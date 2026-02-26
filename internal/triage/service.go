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
	store   Store
	engine  *Engine
	logger  log.Logger
	metrics *Metrics
}

// NewService creates a new triage service. Metrics may be nil.
func NewService(store Store, engine *Engine, logger log.Logger, metrics *Metrics) *Service {
	return &Service{
		store:   store,
		engine:  engine,
		logger:  logger,
		metrics: metrics,
	}
}

// Submit accepts an alert for triage, handling dedup and lifecycle.
func (s *Service) Submit(ctx context.Context, al *alert.Alert) (*SubmitResult, error) {
	// skip resolved alerts
	if al.Status != "firing" {
		s.incSubmit("skipped_not_firing")
		return &SubmitResult{Skipped: true, Reason: "not firing"}, nil
	}

	// dedup: skip if already pending or in progress
	if existing, ok, err := s.store.GetByFingerprint(ctx, al.Fingerprint); err != nil {
		return nil, err
	} else if ok && (existing.Status == StatusPending || existing.Status == StatusInProgress) {
		s.logger.Info(ctx, "triage skipped: active triage exists",
			"fingerprint", al.Fingerprint,
			"alert", al.Labels["alertname"],
			"existing_id", existing.ID,
			"existing_status", existing.Status,
		)
		s.incSubmit("skipped_duplicate")
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

	s.incSubmit("accepted")
	return &SubmitResult{ID: id}, nil
}

func (s *Service) incSubmit(result string) {
	if s.metrics != nil {
		s.metrics.SubmitsTotal.WithLabelValues(result).Inc()
	}
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

	rr := s.engine.Run(ctx, al, s.buildOnTurn(ctx, id))

	result.Status = rr.Status
	result.Analysis = rr.Analysis
	result.Actions = rr.Actions
	result.CompletedAt = rr.CompletedAt
	result.Duration = rr.Duration
	result.TokensUsed = rr.TokensUsed
	result.ToolCalls = rr.ToolCalls
	result.SystemPrompt = rr.SystemPrompt
	result.Model = rr.Model

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

// buildOnTurn returns a TurnCallback that persists each turn incrementally.
// For assistant turns it calls AppendTurn and stashes the returned messageID.
// For user turns (tool results) it calls AppendTurn for the message, then
// AppendToolCalls using the stashed assistant messageID and turn.
func (s *Service) buildOnTurn(ctx context.Context, triageID string) TurnCallback {
	L := s.logger.With("triage_id", triageID)

	var lastAssistantMsgID int
	var lastAssistantSeq int
	var lastAssistantTurn *Turn

	return func(_ context.Context, seq int, turn *Turn) error {
		msgID, err := s.store.AppendTurn(ctx, triageID, seq, turn)
		if err != nil {
			return err
		}

		if turn.Role == "assistant" {
			lastAssistantMsgID = msgID
			lastAssistantSeq = seq
			lastAssistantTurn = turn
			return nil
		}

		// user turn with tool results - attach tool_calls to the preceding assistant message
		if lastAssistantTurn == nil {
			return nil
		}

		toolResults := make(map[string]*ContentBlock)
		for i := range turn.Content {
			block := &turn.Content[i]
			if block.Type == "tool_result" {
				toolResults[block.ToolUseID] = block
			}
		}

		if err := s.store.AppendToolCalls(ctx, triageID, lastAssistantMsgID, lastAssistantSeq, lastAssistantTurn, toolResults); err != nil {
			L.Warn(ctx, "failed to persist tool calls", "seq", seq, "err", err)
		}

		lastAssistantTurn = nil
		return nil
	}
}
