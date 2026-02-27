package relay

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jivansh77/pair-share/internal/session"
)

const (
	MsgTypePTY     byte = 0x00
	MsgTypeControl byte = 0x01
)

type ControlMessage struct {
	Type          string `json:"type"`
	Cols          uint16 `json:"cols,omitempty"`
	Rows          uint16 `json:"rows,omitempty"`
	Role          string `json:"role,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	Guests        int    `json:"guests,omitempty"`
	Error         string `json:"error,omitempty"`
	Label         string `json:"label,omitempty"`
	ScrollbackB64 string `json:"scrollback_b64,omitempty"`
	Access        string `json:"access,omitempty"`
	TTL           string `json:"ttl,omitempty"`
	Token         string `json:"token,omitempty"`
}

type Server struct {
	sessions map[string]*session.Session
	mu       sync.RWMutex
	upgrader websocket.Upgrader
}

func NewServer() *Server {
	return &Server{
		sessions: make(map[string]*session.Session),
		upgrader: websocket.Upgrader{
			CheckOrigin:  func(r *http.Request) bool { return true },
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/session/", s.handleSessionRoute)
	mux.HandleFunc("/ws", s.handleWebSocket)

	go s.cleanupLoop()

	log.Printf("Relay server listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleSessionRoute(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[len("/session/"):]

	if strings.HasSuffix(path, "/log") {
		id := strings.TrimSuffix(path, "/log")
		s.handleSessionLog(w, r, id)
		return
	}
	if strings.HasSuffix(path, "/checkpoint") {
		id := strings.TrimSuffix(path, "/checkpoint")
		s.handleCheckpointAPI(w, r, id)
		return
	}
	if strings.HasSuffix(path, "/summon") {
		id := strings.TrimSuffix(path, "/summon")
		s.handleSummonAPI(w, r, id)
		return
	}
	s.handleSessionInfo(w, r, path)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleSessionInfo(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"id":         sess.ID,
		"guests":     sess.GuestCount(),
		"created_at": sess.CreatedAt.Format(time.RFC3339),
		"watch_only": sess.WatchOnly,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleSessionLog(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	data, err := sess.Log.MarshalJSON()
	if err != nil {
		http.Error(w, "failed to serialize log", http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	role := r.URL.Query().Get("role")

	if sessionID == "" || (role != "host" && role != "guest") {
		http.Error(w, "missing session or invalid role", http.StatusBadRequest)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade error: %v", err)
		return
	}

	switch role {
	case "host":
		s.handleHost(conn, sessionID, r)
	case "guest":
		s.handleGuest(conn, sessionID, r)
	}
}

func (s *Server) handleHost(conn *websocket.Conn, sessionID string, r *http.Request) {
	ttlStr := r.URL.Query().Get("ttl")
	ttl := 4 * time.Hour
	if ttlStr != "" {
		if parsed, err := time.ParseDuration(ttlStr); err == nil {
			ttl = parsed
		}
	}
	watchOnly := r.URL.Query().Get("watch_only") == "true"
	password := r.URL.Query().Get("password")

	s.mu.Lock()
	if _, exists := s.sessions[sessionID]; exists {
		s.mu.Unlock()
		sendControlMsg(conn, ControlMessage{Type: "error", Error: "session ID already in use"})
		conn.Close()
		return
	}

	sess := session.NewSession(sessionID, ttl, watchOnly, password)
	sess.Host = conn
	s.sessions[sessionID] = sess
	s.mu.Unlock()

	log.Printf("Host connected: session=%s", sessionID)

	defer func() {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()

		sess.BroadcastToGuests(makeControlFrame(ControlMessage{Type: "error", Error: "host disconnected"}))
		conn.Close()
		log.Printf("Host disconnected: session=%s", sessionID)
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.BinaryMessage || len(data) == 0 {
			continue
		}

		switch data[0] {
		case MsgTypePTY:
			sess.AppendScrollback(data[1:])
			sess.Log.Append("host", data[1:], false)
			sess.BroadcastToGuests(data)
		case MsgTypeControl:
			sess.Log.Append("host", data[1:], true)
			s.handleHostControl(sess, data[1:])
		}
	}
}

func (s *Server) handleGuest(conn *websocket.Conn, sessionID string, r *http.Request) {
	s.mu.RLock()
	sess, ok := s.sessions[sessionID]
	s.mu.RUnlock()

	if !ok {
		sendControlMsg(conn, ControlMessage{Type: "error", Error: "session not found or expired"})
		conn.Close()
		return
	}

	// Check token-based auth (agent) or password
	token := r.URL.Query().Get("token")
	isAgent := r.URL.Query().Get("role") == "agent"
	agentToken := ""

	if token != "" {
		at, valid := sess.ValidateToken(token)
		if !valid {
			sendControlMsg(conn, ControlMessage{Type: "error", Error: "invalid or expired token"})
			conn.Close()
			return
		}
		isAgent = true
		agentToken = token
		// Token determines access level
		if at.Role == "watch" {
			r.URL.Query().Set("watch", "true")
		}
	} else if sess.Password != "" {
		pw := r.URL.Query().Get("password")
		if pw != sess.Password {
			sendControlMsg(conn, ControlMessage{Type: "error", Error: "invalid password"})
			conn.Close()
			return
		}
	}

	watchMode := r.URL.Query().Get("watch") == "true" || sess.WatchOnly
	if token != "" {
		if at, valid := sess.ValidateToken(token); valid && at.Role == "watch" {
			watchMode = true
		}
	}
	canControl := !watchMode

	sess.AddGuest(conn, canControl, isAgent)
	if isAgent && agentToken != "" {
		sess.SetGuestToken(conn, agentToken)
	}
	guestCount := sess.GuestCount()

	source := "guest"
	if isAgent {
		source = "agent"
		log.Printf("Agent joined: session=%s guests=%d control=%v token=%s", sessionID, guestCount, canControl, agentToken)
	} else {
		log.Printf("Guest joined: session=%s guests=%d control=%v", sessionID, guestCount, canControl)
	}

	sendControlMsg(conn, ControlMessage{
		Type: "resize",
		Cols: sess.TermSize.Cols,
		Rows: sess.TermSize.Rows,
	})

	if scrollback := sess.GetScrollback(); len(scrollback) > 0 {
		frame := MakeDataFrame(scrollback)
		_ = conn.WriteMessage(websocket.BinaryMessage, frame)
	}

	roleStr := "watch"
	if canControl {
		roleStr = "control"
	}
	sendControlMsg(conn, ControlMessage{Type: "role", Role: roleStr})

	if sess.Host != nil {
		infoMsg := ControlMessage{
			Type:   "info",
			Guests: guestCount,
			Role:   roleStr,
		}
		if isAgent {
			infoMsg.Role = "agent/" + roleStr
		}
		sendControlMsg(sess.Host, infoMsg)
	}

	defer func() {
		sess.RemoveGuest(conn)
		conn.Close()
		remaining := sess.GuestCount()
		if sess.Host != nil {
			sendControlMsg(sess.Host, ControlMessage{
				Type:   "info",
				Guests: remaining,
			})
		}
		log.Printf("%s left: session=%s guests=%d", source, sessionID, remaining)
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.BinaryMessage || len(data) == 0 {
			continue
		}

		switch data[0] {
		case MsgTypePTY:
			sess.Log.Append(source, data[1:], false)
			if canControl && sess.Host != nil {
				_ = sess.Host.WriteMessage(websocket.BinaryMessage, data)
			}
		case MsgTypeControl:
			sess.Log.Append(source, data[1:], true)
		}
	}
}

func (s *Server) handleHostControl(sess *session.Session, jsonData []byte) {
	var msg ControlMessage
	if err := json.Unmarshal(jsonData, &msg); err != nil {
		return
	}

	switch msg.Type {
	case "resize":
		sess.TermSize = session.TermSize{Cols: msg.Cols, Rows: msg.Rows}
		frame := makeControlFrame(msg)
		sess.BroadcastToGuests(frame)

	case "checkpoint":
		scrollback := sess.GetScrollback()
		b64 := base64.StdEncoding.EncodeToString(scrollback)
		sendControlMsg(sess.Host, ControlMessage{
			Type:          "checkpoint_ack",
			Label:         msg.Label,
			ScrollbackB64: b64,
		})

	case "summon":
		ttl := 120 * time.Second
		if msg.TTL != "" {
			if parsed, err := time.ParseDuration(msg.TTL); err == nil {
				ttl = parsed
			}
		}
		access := msg.Access
		if access == "" {
			access = "watch"
		}

		token, err := sess.AddToken(access, ttl)
		if err != nil {
			sendControlMsg(sess.Host, ControlMessage{Type: "error", Error: "failed to generate token"})
			return
		}

		sendControlMsg(sess.Host, ControlMessage{
			Type:  "summon_ack",
			Token: token,
		})

		// Auto-expire: close agent connection after TTL
		go func(token string, ttl time.Duration) {
			time.Sleep(ttl)
			sess.CloseAgentByToken(token)
			sess.RemoveToken(token)
			log.Printf("Agent token expired: session=%s token=%s", sess.ID, token)
		}(token, ttl)
	}
}

func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		for id, sess := range s.sessions {
			if sess.IsExpired() {
				sess.BroadcastToGuests(makeControlFrame(ControlMessage{
					Type:  "error",
					Error: "session expired",
				}))
				if sess.Host != nil {
					sess.Host.Close()
				}
				delete(s.sessions, id)
				log.Printf("Session expired: %s", id)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) handleCheckpointAPI(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	scrollback := sess.GetScrollback()
	b64 := base64.StdEncoding.EncodeToString(scrollback)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"scrollback_b64": b64,
		"scrollback_len": len(scrollback),
	})
}

func (s *Server) handleSummonAPI(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	access := r.URL.Query().Get("access")
	if access == "" {
		access = "watch"
	}
	ttlStr := r.URL.Query().Get("ttl")
	ttl := 120 * time.Second
	if ttlStr != "" {
		if parsed, err := time.ParseDuration(ttlStr); err == nil {
			ttl = parsed
		}
	}

	token, err := sess.AddToken(access, ttl)
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	// Auto-expire
	go func(token string, ttl time.Duration) {
		time.Sleep(ttl)
		sess.CloseAgentByToken(token)
		sess.RemoveToken(token)
		log.Printf("Agent token expired: session=%s token=%s", sess.ID, token)
	}(token, ttl)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":  token,
		"access": access,
		"ttl":    ttl.String(),
	})
}

func sendControlMsg(conn *websocket.Conn, msg ControlMessage) {
	data := makeControlFrame(msg)
	_ = conn.WriteMessage(websocket.BinaryMessage, data)
}

func makeControlFrame(msg ControlMessage) []byte {
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return nil
	}
	frame := make([]byte, 1+len(jsonData))
	frame[0] = MsgTypeControl
	copy(frame[1:], jsonData)
	return frame
}

func MakeDataFrame(ptyData []byte) []byte {
	frame := make([]byte, 1+len(ptyData))
	frame[0] = MsgTypePTY
	copy(frame[1:], ptyData)
	return frame
}

func ParseFrame(data []byte) (byte, []byte) {
	if len(data) == 0 {
		return 0, nil
	}
	return data[0], data[1:]
}

func FormatServerAddr(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}
