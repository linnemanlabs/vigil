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

// Put stores a copy of the triage result. If the incoming result has a nil
// Conversation, any previously stored conversation is preserved (so a
// metadata-only Put does not wipe incrementally-built conversation data).
func (s *Store) Put(_ context.Context, r *triage.Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	if cp.Conversation == nil {
		if existing, ok := s.results[r.ID]; ok && existing.Conversation != nil {
			cp.Conversation = existing.Conversation
		}
	}
	s.results[r.ID] = &cp
	s.seen[r.Fingerprint] = r.ID
	return nil
}

// AppendTurn appends a copy of the turn to the stored result's conversation.
// It returns seq as a pseudo message ID.
func (s *Store) AppendTurn(_ context.Context, triageID string, seq int, turn *triage.Turn) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.results[triageID]
	if !ok {
		return 0, nil
	}
	if r.Conversation == nil {
		r.Conversation = &triage.Conversation{}
	}
	cp := *turn
	contentCp := make([]triage.ContentBlock, len(turn.Content))
	copy(contentCp, turn.Content)
	cp.Content = contentCp
	r.Conversation.Turns = append(r.Conversation.Turns, cp)
	return seq, nil
}

// AppendToolCalls is a no-op for the in-memory store; tool data lives in
// the content blocks already stored by AppendTurn.
func (s *Store) AppendToolCalls(_ context.Context, _ string, _, _ int, _ *triage.Turn, _ map[string]*triage.ContentBlock) error {
	return nil
}
