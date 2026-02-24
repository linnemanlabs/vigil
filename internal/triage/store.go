package triage

import "context"

// Store is the persistence interface for triage results.
type Store interface {
	Get(ctx context.Context, id string) (*Result, bool, error)
	GetByFingerprint(ctx context.Context, fingerprint string) (*Result, bool, error)
	Put(ctx context.Context, result *Result) error
}
