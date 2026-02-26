package triage

import "context"

// TurnCallback is invoked after each turn is appended during Engine.Run.
type TurnCallback func(ctx context.Context, seq int, turn *Turn) error

// Notifier sends notifications about completed triages.
type Notifier interface {
	Send(ctx context.Context, result *Result) error
}

type nopNotifier struct{}

func (nopNotifier) Send(context.Context, *Result) error { return nil }

// Store is the persistence interface for triage results.
type Store interface {
	Get(ctx context.Context, id string) (*Result, bool, error)
	GetByFingerprint(ctx context.Context, fingerprint string) (*Result, bool, error)
	Put(ctx context.Context, result *Result) error
	AppendTurn(ctx context.Context, triageID string, seq int, turn *Turn) (messageID int, err error)
	AppendToolCalls(ctx context.Context, triageID string, messageID, messageSeq int, turn *Turn, toolResults map[string]*ContentBlock) error
}
