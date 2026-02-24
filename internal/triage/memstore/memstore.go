// Package memstore provides an in-memory implementation of triage.Store.
package memstore

import (
	"context"
	"sync"

	"github.com/linnemanlabs/vigil/internal/triage"
)

// Store holds triage results in memory. Suitable for dev/testing.
type Store struct {
	mu      sync.RWMutex
	results map[string]*triage.Result // triage ID -> result
	seen    map[string]string         // alert fingerprint -> triage ID (dedup)
}

// New initializes a new in-memory Store.
func New() *Store {
	return &Store{
		results: make(map[string]*triage.Result),
		seen:    make(map[string]string),
	}
}

// Get retrieves a triage result by its ID. Returns a copy.
func (s *Store) Get(_ context.Context, id string) (*triage.Result, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[id]
	if !ok {
		return nil, false, nil
	}
	cp := *r
	return &cp, true, nil
}

// GetByFingerprint retrieves a triage result by alert fingerprint, for deduplication. Returns a copy.
func (s *Store) GetByFingerprint(_ context.Context, fp string) (*triage.Result, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.seen[fp]
	if !ok {
		return nil, false, nil
	}
	r := s.results[id]
	cp := *r
	return &cp, true, nil
}

// Put stores a copy of the triage result.
func (s *Store) Put(_ context.Context, r *triage.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	s.results[r.ID] = &cp
	s.seen[r.Fingerprint] = r.ID
	return nil
}
