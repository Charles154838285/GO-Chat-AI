package sequence

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// SessionSequencer allocates monotonic sequence numbers per session.
type SessionSequencer struct {
	mu       sync.RWMutex
	counters map[string]*sessionCounter
}

type sessionCounter struct {
	value      int64
	lastAccess time.Time
}

var GlobalSequencer = NewSessionSequencer()

func NewSessionSequencer() *SessionSequencer {
	s := &SessionSequencer{counters: make(map[string]*sessionCounter)}
	go s.gcLoop()
	return s
}

// Next returns the next sequence in a session namespace.
func (s *SessionSequencer) Next(sessionID string) int64 {
	if sessionID == "" {
		sessionID = "global"
	}
	s.mu.RLock()
	counter, exists := s.counters[sessionID]
	s.mu.RUnlock()
	if !exists {
		s.mu.Lock()
		if counter, exists = s.counters[sessionID]; !exists {
			counter = &sessionCounter{value: 0, lastAccess: time.Now()}
			s.counters[sessionID] = counter
		}
		s.mu.Unlock()
	}
	counter.lastAccess = time.Now()
	return atomic.AddInt64(&counter.value, 1)
}

func (s *SessionSequencer) TraceID(sessionID, senderID string, seq int64) string {
	if sessionID == "" {
		sessionID = "unknown_session"
	}
	if senderID == "" {
		senderID = "unknown_sender"
	}
	return fmt.Sprintf("trc_%s_%s_%d_%d", sessionID, senderID, seq, time.Now().UnixNano())
}

func (s *SessionSequencer) gcLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		threshold := time.Now().Add(-1 * time.Hour)
		s.mu.Lock()
		for sessionID, counter := range s.counters {
			if counter.lastAccess.Before(threshold) {
				delete(s.counters, sessionID)
			}
		}
		s.mu.Unlock()
	}
}
