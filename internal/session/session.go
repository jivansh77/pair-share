package session

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type TermSize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type GuestConn struct {
	Conn    *websocket.Conn
	HasCtrl bool
}

type Session struct {
	ID        string
	Host      *websocket.Conn
	Guests    map[*websocket.Conn]*GuestConn
	TermSize  TermSize
	CreatedAt time.Time
	TTL       time.Duration
	Password  string // bcrypt hash, empty if unset
	WatchOnly bool   // default mode for guests

	mu sync.RWMutex
}

func NewSession(id string, ttl time.Duration, watchOnly bool, password string) *Session {
	return &Session{
		ID:        id,
		Guests:    make(map[*websocket.Conn]*GuestConn),
		CreatedAt: time.Now(),
		TTL:       ttl,
		WatchOnly: watchOnly,
		Password:  password,
	}
}

func (s *Session) IsExpired() bool {
	return time.Since(s.CreatedAt) > s.TTL
}

func (s *Session) AddGuest(conn *websocket.Conn, canControl bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Guests[conn] = &GuestConn{Conn: conn, HasCtrl: canControl}
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
