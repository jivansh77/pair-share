package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	actlog "github.com/jivansh77/pair-share/internal/log"
)

type TermSize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type GuestConn struct {
	Conn    *websocket.Conn
	HasCtrl bool
	IsAgent bool
	Token   string // agent token, empty for human guests
}

type AgentToken struct {
	Token     string
	Role      string // "watch" or "control"
	ExpiresAt time.Time
}

const ScrollbackSize = 64 * 1024 // 64KB ring buffer

type Session struct {
	ID        string
	Host      *websocket.Conn
	Guests    map[*websocket.Conn]*GuestConn
	TermSize  TermSize
	CreatedAt time.Time
	TTL       time.Duration
	Password  string // bcrypt hash, empty if unset
	WatchOnly bool   // default mode for guests
	Log       *actlog.ActivityLog
	Tokens    map[string]*AgentToken

	scrollback []byte
	mu         sync.RWMutex
}

func NewSession(id string, ttl time.Duration, watchOnly bool, password string) *Session {
	return &Session{
		ID:        id,
		Guests:    make(map[*websocket.Conn]*GuestConn),
		CreatedAt: time.Now(),
		TTL:       ttl,
		WatchOnly: watchOnly,
		Password:  password,
		Log:       actlog.NewActivityLog(),
		Tokens:    make(map[string]*AgentToken),
	}
}

func (s *Session) IsExpired() bool {
	return time.Since(s.CreatedAt) > s.TTL
}

func (s *Session) AddGuest(conn *websocket.Conn, canControl bool, isAgent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Guests[conn] = &GuestConn{Conn: conn, HasCtrl: canControl, IsAgent: isAgent}
}

func (s *Session) SetGuestToken(conn *websocket.Conn, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.Guests[conn]; ok {
		g.Token = token
	}
}

func (s *Session) IsAgentConn(conn *websocket.Conn) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if g, ok := s.Guests[conn]; ok {
		return g.IsAgent
	}
	return false
}

func (s *Session) RemoveGuest(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Guests, conn)
}

func (s *Session) GuestCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Guests)
}

func (s *Session) BroadcastToGuests(data []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.Guests {
		_ = g.Conn.WriteMessage(websocket.BinaryMessage, data)
	}
}

// AppendScrollback adds PTY data to the ring buffer.
// Only stores the raw PTY bytes (without the 0x00 frame prefix).
func (s *Session) AppendScrollback(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.scrollback = append(s.scrollback, data...)
	if len(s.scrollback) > ScrollbackSize {
		// Trim to keep only the last ScrollbackSize bytes
		s.scrollback = s.scrollback[len(s.scrollback)-ScrollbackSize:]
	}
}

// GetScrollback returns a copy of the current scrollback buffer.
func (s *Session) GetScrollback() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.scrollback) == 0 {
		return nil
	}
	result := make([]byte, len(s.scrollback))
	copy(result, s.scrollback)
	return result
}

func GenerateID() (string, error) {
	adj, err := randElement(adjectives)
	if err != nil {
		return "", err
	}
	animal, err := randElement(animals)
	if err != nil {
		return "", err
	}
	num, err := randRange(10, 99)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%d", adj, animal, num), nil
}

func randElement(list []string) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	if err != nil {
		return "", err
	}
	return list[n.Int64()], nil
}

func randRange(min, max int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()) + min, nil
}

func (s *Session) AddToken(role string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := "tok_" + hex.EncodeToString(b)

	s.Tokens[token] = &AgentToken{
		Token:     token,
		Role:      role,
		ExpiresAt: time.Now().Add(ttl),
	}
	return token, nil
}

func (s *Session) ValidateToken(token string) (*AgentToken, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.Tokens[token]
	if !ok || time.Now().After(t.ExpiresAt) {
		return nil, false
	}
	return t, true
}

func (s *Session) RemoveToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Tokens, token)
}

// CloseAgentByToken closes the agent connection that used the given token.
func (s *Session) CloseAgentByToken(token string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.Guests {
		if g.IsAgent && g.Token == token {
			g.Conn.Close()
			return
		}
	}
}
