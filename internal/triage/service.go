package triage

import (
	"context"
	"time"

	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/vigil/internal/alert"
	"github.com/oklog/ulid/v2"
)

// SubmitResult is the outcome of submitting an alert for triage.
type SubmitResult struct {
	ID      string
	Skipped bool
	Reason  string
}

// Service is the business boundary for triage operations.
type Service struct {
	store  Store
	engine *Engine
	logger log.Logger
}

// NewService creates a new triage service.
func NewService(store Store, engine *Engine, logger log.Logger) *Service {
	return &Service{
		store:  store,
		engine: engine,
		logger: logger,
	}
}

// Submit accepts an alert for triage, handling dedup and lifecycle.
func (s *Service) Submit(ctx context.Context, al *alert.Alert) (*SubmitResult, error) {
	// skip resolved alerts
	if al.Status != "firing" {
		return &SubmitResult{Skipped: true, Reason: "not firing"}, nil
	}

	// dedup: skip if already pending or in progress
	if existing, ok, err := s.store.GetByFingerprint(ctx, al.Fingerprint); err != nil {
		return nil, err
	} else if ok && (existing.Status == StatusPending || existing.Status == StatusInProgress) {
		return &SubmitResult{Skipped: true, Reason: "duplicate"}, nil
	}

	id := ulid.Make().String()
	result := &Result{
		ID:          id,
		Fingerprint: al.Fingerprint,
		Status:      StatusPending,
		Alert:       al.Labels["alertname"],
		Severity:    al.Labels["severity"],
		Summary:     al.Annotations["summary"],
		CreatedAt:   time.Now(),
	}

	if err := s.store.Put(ctx, result); err != nil {
		return nil, err
	}

	// kick off async triage - pass only the ID to avoid sharing the Result pointer.
	go s.runTriage(context.WithoutCancel(ctx), id, al)

	return &SubmitResult{ID: id}, nil
}

// Get retrieves a triage result by ID.
func (s *Service) Get(ctx context.Context, id string) (*Result, bool, error) {
	return s.store.Get(ctx, id)
}

func (s *Service) runTriage(ctx context.Context, id string, al *alert.Alert) {
	L := s.logger.With("triage_id", id, "alert", al.Labels["alertname"])

	result, ok, err := s.store.Get(ctx, id)
	if err != nil || !ok {
		L.Error(ctx, err, "failed to fetch result for triage")
		return
	}

	result.Status = StatusInProgress
	if err := s.store.Put(ctx, result); err != nil {
		L.Error(ctx, err, "failed to update status to in_progress")
		return
	}

	rr := s.engine.Run(ctx, al)

	result.Status = rr.Status
	result.Analysis = rr.Analysis
	result.Actions = rr.Actions
	result.Conversation = rr.Conversation
	result.CompletedAt = rr.CompletedAt
	result.Duration = rr.Duration
	result.TokensUsed = rr.TokensUsed
	result.ToolCalls = rr.ToolCalls

	if err := s.store.Put(ctx, result); err != nil {
		L.Error(ctx, err, "failed to persist triage result")
	}

	L.Info(ctx, "triage complete",
		"status", rr.Status,
		"duration", rr.Duration,
		"tokens", rr.TokensUsed,
		"tool_calls", rr.ToolCalls,
	)
}
