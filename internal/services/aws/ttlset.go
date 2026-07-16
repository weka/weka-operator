package aws

import (
	"sync"
	"time"
)

// TTLSet is a concurrency-safe set of string keys that expire a fixed TTL after they are marked.
// Entries are pruned lazily on read (Has). It backs the operator's in-memory lifecycle caches — e.g.
// instances already released, or nodes/ASGs whose termination hook has been verified — keeping
// steady-state reconciles free of repeated AWS calls while still re-asserting after the TTL to repair
// drift. A nil TTLSet must not be used; construct with NewTTLSet.
type TTLSet struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]time.Time
}

// NewTTLSet returns an empty TTLSet whose entries expire ttl after they are marked.
func NewTTLSet(ttl time.Duration) *TTLSet {
	return &TTLSet{ttl: ttl, m: map[string]time.Time{}}
}

// Has reports whether key was marked within the TTL. Expired entries are pruned on read.
func (s *TTLSet) Has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[key]
	if !ok {
		return false
	}
	if time.Since(t) >= s.ttl {
		delete(s.m, key)
		return false
	}
	return true
}

// Mark records key as present, (re)starting its TTL from now.
func (s *TTLSet) Mark(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = time.Now()
}

// Reset drops all entries. Intended for tests that need a clean cache between cases.
func (s *TTLSet) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = map[string]time.Time{}
}
