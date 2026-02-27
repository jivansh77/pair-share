package log

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"
)

const MaxEntries = 10_000

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"` // "host", "guest", "agent"
	Data      []byte   `json:"-"`
	DataB64   string   `json:"data_b64,omitempty"`
	SizeBytes int      `json:"size_bytes"`
	IsControl bool     `json:"is_control"`
}

type ActivityLog struct {
	entries []LogEntry
	mu      sync.RWMutex
}

func NewActivityLog() *ActivityLog {
	return &ActivityLog{
		entries: make([]LogEntry, 0, 1024),
	}
}

func (l *ActivityLog) Append(source string, data []byte, isControl bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	entry := LogEntry{
		Timestamp: time.Now(),
		Source:    source,
		Data:      dataCopy,
		SizeBytes: len(data),
		IsControl: isControl,
	}

	l.entries = append(l.entries, entry)
	if len(l.entries) > MaxEntries {
		l.entries = l.entries[len(l.entries)-MaxEntries:]
	}
}

func (l *ActivityLog) Entries() []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	result := make([]LogEntry, len(l.entries))
	copy(result, l.entries)
	return result
}

func (l *ActivityLog) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

// MarshalJSON returns the activity log as a JSON array with base64-encoded data.
func (l *ActivityLog) MarshalJSON() ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	export := make([]LogEntry, len(l.entries))
	for i, e := range l.entries {
		export[i] = LogEntry{
			Timestamp: e.Timestamp,
			Source:    e.Source,
			DataB64:   base64.StdEncoding.EncodeToString(e.Data),
			SizeBytes: e.SizeBytes,
			IsControl: e.IsControl,
		}
	}
	return json.Marshal(export)
}
