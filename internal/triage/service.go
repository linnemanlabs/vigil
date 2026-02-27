package triage

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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
	store    Store
	engine   *Engine
	logger   log.Logger
	metrics  *Metrics
	notifier Notifier
}

// NewService creates a new triage service. Metrics and notifier may be nil.
func NewService(store Store, engine *Engine, logger log.Logger, metrics *Metrics, notifier Notifier) *Service {
	if notifier == nil {
		notifier = nopNotifier{}
	}
	return &Service{
		store:    store,
		engine:   engine,
		logger:   logger,
		metrics:  metrics,
		notifier: notifier,
	}
}

// Submit accepts an alert for triage, handling dedup and lifecycle.
//
//nolint:spancheck // triageSpan is ended in the runTriage goroutine via defer
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

	// Start a new root span for the triage, linked back to the HTTP request span.
	// The span is ended in runTriage via defer; spancheck can't see across goroutines.
	httpSpanCtx := trace.SpanFromContext(ctx).SpanContext()
	triageCtx, triageSpan := tracer.Start(
		context.WithoutCancel(ctx),
		"triage",
		trace.WithNewRoot(),
		trace.WithLinks(trace.Link{SpanContext: httpSpanCtx}),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.provider.name", "anthropic"),
			attribute.String("gen_ai.agent.name", "vigil"),
			attribute.String("vigil.triage.id", id),
			attribute.String("vigil.alert.name", al.Labels["alertname"]),
			attribute.String("vigil.alert.fingerprint", al.Fingerprint),
			attribute.String("vigil.triage.severity", al.Labels["severity"]),
		),
	)

	go s.runTriage(triageCtx, id, al, triageSpan)

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

func (s *Service) runTriage(ctx context.Context, id string, al *alert.Alert, triageSpan trace.Span) {
	defer triageSpan.End()

	L := s.logger.With("triage_id", id, "alert", al.Labels["alertname"])

	result, ok, err := s.store.Get(ctx, id)
	if err != nil || !ok {
		L.Error(ctx, err, "failed to fetch result for triage")
		triageSpan.RecordError(err)
		triageSpan.SetStatus(codes.Error, "failed to fetch result")
		return
	}

	result.Status = StatusInProgress
	if err := s.store.Put(ctx, result); err != nil {
		L.Error(ctx, err, "failed to update status to in_progress")
		triageSpan.RecordError(err)
		triageSpan.SetStatus(codes.Error, "failed to update status")
		return
	}

	rr := s.engine.Run(ctx, id, al, s.buildOnTurn(ctx, id))

	result.Status = rr.Status
	result.Analysis = rr.Analysis
	result.ToolsUsed = rr.ToolsUsed
	result.CompletedAt = rr.CompletedAt
	result.Duration = rr.Duration
	result.LLMTime = rr.LLMTime
	result.ToolTime = rr.ToolTime
	result.TokensUsed = rr.TokensUsed
	result.ToolCalls = rr.ToolCalls
	result.SystemPrompt = rr.SystemPrompt
	result.Model = rr.Model

	if err := s.store.Put(ctx, result); err != nil {
		L.Error(ctx, err, "failed to persist triage result")
	}

	triageSpan.SetAttributes(
		attribute.String("gen_ai.response.model", rr.Model),
		attribute.Int("gen_ai.usage.input_tokens", rr.InputTokensUsed),
		attribute.Int("gen_ai.usage.output_tokens", rr.OutputTokensUsed),
		attribute.String("vigil.triage.status", string(rr.Status)),
		attribute.Int("vigil.triage.tool_calls", rr.ToolCalls),
	)
	if rr.Status == StatusFailed {
		triageSpan.SetStatus(codes.Error, rr.Analysis)
	}

	if err := s.notifier.Send(ctx, result); err != nil {
		L.Warn(ctx, "notification failed", "err", err)
	} else {
		L.Info(ctx, "notification sent", "triage_id", id)
	}

	L.Info(ctx, "triage complete",
		"status", rr.Status,
		"duration", rr.Duration,
		"llm_time", rr.LLMTime,
		"tool_time", rr.ToolTime,
		"tokens", rr.TokensUsed,
		"tool_calls", rr.ToolCalls,
		"model", rr.Model,
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
