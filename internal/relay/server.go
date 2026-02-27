package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	Type      string `json:"type"`
	Cols      uint16 `json:"cols,omitempty"`
	Rows      uint16 `json:"rows,omitempty"`
	Role      string `json:"role,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Guests    int    `json:"guests,omitempty"`
	Error     string `json:"error,omitempty"`
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
	mux.HandleFunc("/session/", s.handleSessionInfo)
	mux.HandleFunc("/ws", s.handleWebSocket)

	go s.cleanupLoop()

	log.Printf("Relay server listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleSessionInfo(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/session/"):]
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
			sess.BroadcastToGuests(data)
		case MsgTypeControl:
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

	if sess.Password != "" {
		pw := r.URL.Query().Get("password")
		if pw != sess.Password {
			sendControlMsg(conn, ControlMessage{Type: "error", Error: "invalid password"})
			conn.Close()
			return
		}
	}

	watchMode := r.URL.Query().Get("watch") == "true" || sess.WatchOnly
	canControl := !watchMode

	sess.AddGuest(conn, canControl)
	guestCount := sess.GuestCount()

	log.Printf("Guest joined: session=%s guests=%d control=%v", sessionID, guestCount, canControl)

	sendControlMsg(conn, ControlMessage{
		Type: "resize",
		Cols: sess.TermSize.Cols,
		Rows: sess.TermSize.Rows,
	})

	roleStr := "watch"
	if canControl {
		roleStr = "control"
	}
	sendControlMsg(conn, ControlMessage{Type: "role", Role: roleStr})

	// Notify host about guest join
	if sess.Host != nil {
		sendControlMsg(sess.Host, ControlMessage{
			Type:   "info",
			Guests: guestCount,
			Role:   roleStr,
		})
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
		log.Printf("Guest left: session=%s guests=%d", sessionID, remaining)
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
			if canControl && sess.Host != nil {
				_ = sess.Host.WriteMessage(websocket.BinaryMessage, data)
			}
		case MsgTypeControl:
			// Guests can only send resize for now
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
