package triage

import "sync"

// Store holds triage results in memory. POC, no persistence for v1.
type Store struct {
	mu      sync.RWMutex
	results map[string]*Result // triage ID -> result
	seen    map[string]string  // alert fingerprint -> triage ID (dedup)
}

// NewStore initializes a new in-memory Store.
func NewStore() *Store {
	return &Store{
		results: make(map[string]*Result),
		seen:    make(map[string]string),
	}
}

// Get retrieves a triage result by its ID.
func (s *Store) Get(id string) (*Result, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[id]
	return r, ok
}

// GetByFingerprint retrieves a triage result by alert fingerprint, for deduplication.
func (s *Store) GetByFingerprint(fp string) (*Result, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.seen[fp]
	if !ok {
		return nil, false
	}
	return s.results[id], true
}

// Put adds or updates a triage result in the store. It also updates the seen map for deduplication
func (s *Store) Put(r *Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[r.ID] = r
	s.seen[r.Fingerprint] = r.ID
}
