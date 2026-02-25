package memstore

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/linnemanlabs/vigil/internal/triage"
)

func TestStore_PutAndGet(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	r := &triage.Result{ID: "t-1", Fingerprint: "fp-1", Status: triage.StatusPending}
	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get(ctx, "t-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected result to be found")
	}
	if got.ID != "t-1" {
		t.Errorf("ID = %q, want %q", got.ID, "t-1")
	}
	if got.Fingerprint != "fp-1" {
		t.Errorf("Fingerprint = %q, want %q", got.Fingerprint, "fp-1")
	}
}

func TestStore_GetMissing(t *testing.T) {
	t.Parallel()

	s := New()
	_, ok, err := s.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for missing ID")
	}
}

func TestStore_GetByFingerprint(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	r := &triage.Result{ID: "t-2", Fingerprint: "fp-abc", Status: triage.StatusPending}
	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.GetByFingerprint(ctx, "fp-abc")
	if err != nil {
		t.Fatalf("GetByFingerprint: %v", err)
	}
	if !ok {
		t.Fatal("expected result to be found by fingerprint")
	}
	if got.ID != "t-2" {
		t.Errorf("ID = %q, want %q", got.ID, "t-2")
	}
}

func TestStore_GetByFingerprintMissing(t *testing.T) {
	t.Parallel()

	s := New()
	_, ok, err := s.GetByFingerprint(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetByFingerprint: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for missing fingerprint")
	}
}

func TestStore_PutOverwrites(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	_ = s.Put(ctx, &triage.Result{ID: "t-3", Fingerprint: "fp-3", Status: triage.StatusPending})
	_ = s.Put(ctx, &triage.Result{ID: "t-3", Fingerprint: "fp-3", Status: triage.StatusComplete, Analysis: "done"})

	got, ok, err := s.Get(ctx, "t-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected result to be found")
	}
	if got.Status != triage.StatusComplete {
		t.Errorf("Status = %q, want %q", got.Status, triage.StatusComplete)
	}
	if got.Analysis != "done" {
		t.Errorf("Analysis = %q, want %q", got.Analysis, "done")
	}
}

func TestStore_AppendTurn(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	_ = s.Put(ctx, &triage.Result{ID: "t-at", Fingerprint: "fp-at", Status: triage.StatusInProgress})

	turn1 := &triage.Turn{
		Role:    "assistant",
		Content: []triage.ContentBlock{{Type: "text", Text: "hello"}},
	}
	turn2 := &triage.Turn{
		Role:    "user",
		Content: []triage.ContentBlock{{Type: "tool_result", ToolUseID: "x", Content: "ok"}},
	}

	if _, err := s.AppendTurn(ctx, "t-at", 0, turn1); err != nil {
		t.Fatalf("AppendTurn 0: %v", err)
	}
	if _, err := s.AppendTurn(ctx, "t-at", 1, turn2); err != nil {
		t.Fatalf("AppendTurn 1: %v", err)
	}

	got, ok, err := s.Get(ctx, "t-at")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected result")
	}
	if got.Conversation == nil {
		t.Fatal("expected conversation")
	}
	if len(got.Conversation.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(got.Conversation.Turns))
	}
	if got.Conversation.Turns[0].Role != "assistant" {
		t.Errorf("turn 0 role = %q, want assistant", got.Conversation.Turns[0].Role)
	}
	if got.Conversation.Turns[1].Role != "user" {
		t.Errorf("turn 1 role = %q, want user", got.Conversation.Turns[1].Role)
	}
}

func TestStore_PutPreservesConversation(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	_ = s.Put(ctx, &triage.Result{ID: "t-pc", Fingerprint: "fp-pc", Status: triage.StatusInProgress})

	// Append a turn
	turn := &triage.Turn{Role: "assistant", Content: []triage.ContentBlock{{Type: "text", Text: "hi"}}}
	_, _ = s.AppendTurn(ctx, "t-pc", 0, turn)

	// Put without conversation should preserve existing
	_ = s.Put(ctx, &triage.Result{ID: "t-pc", Fingerprint: "fp-pc", Status: triage.StatusComplete, Analysis: "done"})

	got, _, _ := s.Get(ctx, "t-pc")
	if got.Conversation == nil || len(got.Conversation.Turns) != 1 {
		t.Fatal("Put without conversation should preserve existing turns")
	}
	if got.Analysis != "done" {
		t.Errorf("Analysis = %q, want %q", got.Analysis, "done")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	const n = 100

	var wg sync.WaitGroup
	wg.Add(n * 2)

	for i := range n {
		id := fmt.Sprintf("id-%d", i)
		fp := fmt.Sprintf("fp-%d", i)

		go func() {
			defer wg.Done()
			_ = s.Put(ctx, &triage.Result{ID: id, Fingerprint: fp, Status: triage.StatusPending})
		}()

		go func() {
			defer wg.Done()
			_, _, _ = s.Get(ctx, id)
			_, _, _ = s.GetByFingerprint(ctx, fp)
		}()
	}

	wg.Wait()
}
