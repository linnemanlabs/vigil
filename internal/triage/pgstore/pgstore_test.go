package pgstore_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/linnemanlabs/vigil/internal/triage"
	"github.com/linnemanlabs/vigil/internal/triage/pgstore"
)

func openStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := os.Getenv("VIGIL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("VIGIL_TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestPutAndGet(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond).UTC()
	r := &triage.Result{
		ID:          "test-put-get-001",
		Fingerprint: "fp-put-get",
		Status:      triage.StatusPending,
		Alert:       "HighCPU",
		Severity:    "critical",
		Summary:     "CPU too high",
		Analysis:    "Looks like a runaway process",
		ToolsUsed:   []string{"query_logs", "query_metrics"},
		CreatedAt:   now,
		Duration:    1.23,
		LLMTime:     0.85,
		ToolTime:    0.38,
		TokensUsed:  500,
		ToolCalls:   3,
	}

	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get returned ok=false, want true")
	}

	assertEqual(t, "ID", r.ID, got.ID)
	assertEqual(t, "Fingerprint", r.Fingerprint, got.Fingerprint)
	assertEqual(t, "Status", string(r.Status), string(got.Status))
	assertEqual(t, "Alert", r.Alert, got.Alert)
	assertEqual(t, "Severity", r.Severity, got.Severity)
	assertEqual(t, "Summary", r.Summary, got.Summary)
	assertEqual(t, "Analysis", r.Analysis, got.Analysis)
	assertEqual(t, "Duration", r.Duration, got.Duration)
	assertEqual(t, "LLMTime", r.LLMTime, got.LLMTime)
	assertEqual(t, "ToolTime", r.ToolTime, got.ToolTime)
	assertEqual(t, "TokensUsed", r.TokensUsed, got.TokensUsed)
	assertEqual(t, "ToolCalls", r.ToolCalls, got.ToolCalls)

	if len(got.ToolsUsed) != 2 || got.ToolsUsed[0] != "query_logs" || got.ToolsUsed[1] != "query_metrics" {
		t.Errorf("ToolsUsed mismatch: got %v", got.ToolsUsed)
	}
}

func TestGetMissing(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	_, ok, err := s.Get(ctx, "nonexistent-id")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("Get returned ok=true for nonexistent ID")
	}
}

func TestGetByFingerprint(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	fp := "fp-by-fp-test"
	now := time.Now().Truncate(time.Microsecond).UTC()

	older := &triage.Result{
		ID:          "test-fp-older",
		Fingerprint: fp,
		Status:      triage.StatusComplete,
		CreatedAt:   now.Add(-time.Hour),
	}
	newer := &triage.Result{
		ID:          "test-fp-newer",
		Fingerprint: fp,
		Status:      triage.StatusPending,
		CreatedAt:   now,
	}

	if err := s.Put(ctx, older); err != nil {
		t.Fatalf("Put older: %v", err)
	}
	if err := s.Put(ctx, newer); err != nil {
		t.Fatalf("Put newer: %v", err)
	}

	got, ok, err := s.GetByFingerprint(ctx, fp)
	if err != nil {
		t.Fatalf("GetByFingerprint: %v", err)
	}
	if !ok {
		t.Fatal("GetByFingerprint returned ok=false")
	}
	if got.ID != newer.ID {
		t.Errorf("GetByFingerprint returned ID=%s, want %s", got.ID, newer.ID)
	}
}

func TestGetByFingerprintMissing(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	_, ok, err := s.GetByFingerprint(ctx, "nonexistent-fp")
	if err != nil {
		t.Fatalf("GetByFingerprint: %v", err)
	}
	if ok {
		t.Error("GetByFingerprint returned ok=true for nonexistent fingerprint")
	}
}

func TestUpsert(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond).UTC()
	r := &triage.Result{
		ID:          "test-upsert-001",
		Fingerprint: "fp-upsert",
		Status:      triage.StatusPending,
		CreatedAt:   now,
	}
	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put initial: %v", err)
	}

	completed := now.Add(time.Minute)
	r.Status = triage.StatusComplete
	r.Summary = "resolved"
	r.CompletedAt = completed
	r.Duration = 60.0
	r.LLMTime = 45.0
	r.ToolTime = 12.5
	r.TokensUsed = 1200
	r.ToolCalls = 5

	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put update: %v", err)
	}

	got, ok, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if !ok {
		t.Fatal("Get returned ok=false after upsert")
	}

	assertEqual(t, "Status", string(triage.StatusComplete), string(got.Status))
	assertEqual(t, "Summary", "resolved", got.Summary)
	assertEqual(t, "Duration", 60.0, got.Duration)
	assertEqual(t, "LLMTime", 45.0, got.LLMTime)
	assertEqual(t, "ToolTime", 12.5, got.ToolTime)
	assertEqual(t, "TokensUsed", 1200, got.TokensUsed)
	assertEqual(t, "ToolCalls", 5, got.ToolCalls)
}

func TestConversationRoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond).UTC()

	r := &triage.Result{
		ID:          "test-conv-001",
		Fingerprint: "fp-conv",
		Status:      triage.StatusComplete,
		CreatedAt:   now,
	}
	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Append turns incrementally (as the engine would).
	turns := []triage.Turn{
		{
			Role: "user",
			Content: []triage.ContentBlock{
				{Type: "text", Text: "Analyze this alert"},
			},
			Timestamp: now,
		},
		{
			Role: "assistant",
			Content: []triage.ContentBlock{
				{Type: "text", Text: "Looking into it..."},
				{Type: "tool_use", ID: "tu_1", Name: "query_prometheus", Input: json.RawMessage(`{"query":"up"}`)},
			},
			Timestamp: now.Add(time.Second),
			Usage:     &triage.Usage{InputTokens: 100, OutputTokens: 50},
		},
		{
			Role: "user",
			Content: []triage.ContentBlock{
				{Type: "tool_result", ToolUseID: "tu_1", Content: "up=1"},
			},
			Timestamp: now.Add(2 * time.Second),
		},
	}

	for seq := range turns {
		if _, err := s.AppendTurn(ctx, r.ID, seq, &turns[seq]); err != nil {
			t.Fatalf("AppendTurn seq %d: %v", seq, err)
		}
	}

	got, ok, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get returned ok=false")
	}
	if got.Conversation == nil {
		t.Fatal("Conversation is nil after round-trip")
	}
	if len(got.Conversation.Turns) != 3 {
		t.Fatalf("Conversation turns: got %d, want 3", len(got.Conversation.Turns))
	}

	// Verify tool_use turn content blocks
	assistantTurn := got.Conversation.Turns[1]
	if len(assistantTurn.Content) != 2 {
		t.Fatalf("assistant turn content blocks: got %d, want 2", len(assistantTurn.Content))
	}
	if assistantTurn.Content[1].Name != "query_prometheus" {
		t.Errorf("tool_use name: got %q, want %q", assistantTurn.Content[1].Name, "query_prometheus")
	}
	if assistantTurn.Usage == nil {
		t.Fatal("assistant turn usage is nil")
	}
	if assistantTurn.Usage.InputTokens != 100 {
		t.Errorf("input tokens: got %d, want 100", assistantTurn.Usage.InputTokens)
	}

	// Verify tool_result turn
	toolResultTurn := got.Conversation.Turns[2]
	if toolResultTurn.Content[0].ToolUseID != "tu_1" {
		t.Errorf("tool_result tool_use_id: got %q, want %q", toolResultTurn.Content[0].ToolUseID, "tu_1")
	}
}

func TestAppendTurnAndToolCalls(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond).UTC()
	r := &triage.Result{
		ID:          "test-append-tc-001",
		Fingerprint: "fp-append-tc",
		Status:      triage.StatusInProgress,
		CreatedAt:   now,
	}
	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Append assistant turn with tool_use
	assistantTurn := triage.Turn{
		Role: "assistant",
		Content: []triage.ContentBlock{
			{Type: "text", Text: "Let me check..."},
			{Type: "tool_use", ID: "tc_1", Name: "query_prom", Input: json.RawMessage(`{"q":"up"}`)},
		},
		Timestamp: now.Add(time.Second),
		Usage:     &triage.Usage{InputTokens: 50, OutputTokens: 25},
	}
	msgID, err := s.AppendTurn(ctx, r.ID, 0, &assistantTurn)
	if err != nil {
		t.Fatalf("AppendTurn assistant: %v", err)
	}

	// Append user turn with tool results
	userTurn := triage.Turn{
		Role: "user",
		Content: []triage.ContentBlock{
			{Type: "tool_result", ToolUseID: "tc_1", Content: "up=1"},
		},
		Timestamp: now.Add(2 * time.Second),
	}
	if _, err := s.AppendTurn(ctx, r.ID, 1, &userTurn); err != nil {
		t.Fatalf("AppendTurn user: %v", err)
	}

	// Now append tool_calls
	toolResults := map[string]*triage.ContentBlock{
		"tc_1": {Type: "tool_result", ToolUseID: "tc_1", Content: "up=1"},
	}
	if err := s.AppendToolCalls(ctx, r.ID, msgID, 0, &assistantTurn, toolResults); err != nil {
		t.Fatalf("AppendToolCalls: %v", err)
	}

	// Round-trip via Get
	got, ok, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get returned ok=false")
	}
	if got.Conversation == nil {
		t.Fatal("Conversation is nil")
	}
	if len(got.Conversation.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(got.Conversation.Turns))
	}
	assertEqual(t, "turn[0].Role", "assistant", got.Conversation.Turns[0].Role)
	assertEqual(t, "turn[1].Role", "user", got.Conversation.Turns[1].Role)
}

func assertEqual[T comparable](t *testing.T, field string, want, got T) {
	t.Helper()
	if want != got {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}
