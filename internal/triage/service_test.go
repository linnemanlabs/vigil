package triage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/vigil/internal/alert"
)

// mockStore implements Store for testing.
type mockStore struct {
	mu      sync.Mutex
	results map[string]*Result
	seen    map[string]*Result
	putErr  error
	getErr  error
}

func newMockStore() *mockStore {
	return &mockStore{
		results: make(map[string]*Result),
		seen:    make(map[string]*Result),
	}
}

func (m *mockStore) Get(_ context.Context, id string) (*Result, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, false, m.getErr
	}
	r, ok := m.results[id]
	if !ok {
		return nil, false, nil
	}
	cp := *r
	return &cp, true, nil
}

func (m *mockStore) GetByFingerprint(_ context.Context, fp string) (*Result, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, false, m.getErr
	}
	r, ok := m.seen[fp]
	if !ok {
		return nil, false, nil
	}
	cp := *r
	return &cp, true, nil
}

func (m *mockStore) Put(_ context.Context, r *Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	cp := *r
	if cp.Conversation == nil {
		if existing, ok := m.results[r.ID]; ok && existing.Conversation != nil {
			cp.Conversation = existing.Conversation
		}
	}
	m.results[r.ID] = &cp
	m.seen[r.Fingerprint] = &cp
	return nil
}

func (m *mockStore) AppendTurn(_ context.Context, triageID string, seq int, turn *Turn) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.results[triageID]
	if !ok {
		return 0, nil
	}
	if r.Conversation == nil {
		r.Conversation = &Conversation{}
	}
	cp := *turn
	r.Conversation.Turns = append(r.Conversation.Turns, cp)
	return seq, nil
}

func (m *mockStore) AppendToolCalls(_ context.Context, _ string, _, _ int, _ *Turn, _ map[string]*ContentBlock) error {
	return nil
}

// mockNotifier tracks Send calls for testing.
type mockNotifier struct {
	mu     sync.Mutex
	calls  int
	last   *Result
	err    error
	called chan struct{}
}

func newMockNotifier() *mockNotifier {
	return &mockNotifier{called: make(chan struct{}, 1)}
}

func (m *mockNotifier) Send(_ context.Context, r *Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.last = r
	select {
	case m.called <- struct{}{}:
	default:
	}
	return m.err
}

func TestSubmit_SkipsResolvedAlerts(t *testing.T) {
	t.Parallel()

	svc := NewService(newMockStore(), NewEngine(&mockProvider{}, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider()), log.Nop(), nil, nil, noop.NewTracerProvider())

	sr, err := svc.Submit(context.Background(), &alert.Alert{Status: "resolved"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !sr.Skipped {
		t.Error("expected resolved alert to be skipped")
	}
	if sr.Reason != "not firing" {
		t.Errorf("reason = %q, want %q", sr.Reason, "not firing")
	}
}

func TestSubmit_DedupPending(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	store.seen["fp-1"] = &Result{ID: "existing", Fingerprint: "fp-1", Status: StatusPending}
	store.results["existing"] = store.seen["fp-1"]

	svc := NewService(store, NewEngine(&mockProvider{}, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider()), log.Nop(), nil, nil, noop.NewTracerProvider())

	sr, err := svc.Submit(context.Background(), &alert.Alert{
		Status:      "firing",
		Fingerprint: "fp-1",
		Labels:      map[string]string{"alertname": "Test"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !sr.Skipped {
		t.Error("expected duplicate pending to be skipped")
	}
	if sr.Reason != "duplicate" {
		t.Errorf("reason = %q, want %q", sr.Reason, "duplicate")
	}
}

func TestSubmit_DedupInProgress(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	store.seen["fp-2"] = &Result{ID: "existing", Fingerprint: "fp-2", Status: StatusInProgress}
	store.results["existing"] = store.seen["fp-2"]

	svc := NewService(store, NewEngine(&mockProvider{}, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider()), log.Nop(), nil, nil, noop.NewTracerProvider())

	sr, err := svc.Submit(context.Background(), &alert.Alert{
		Status:      "firing",
		Fingerprint: "fp-2",
		Labels:      map[string]string{"alertname": "Test"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !sr.Skipped {
		t.Error("expected duplicate in_progress to be skipped")
	}
}

func TestSubmit_AllowsRetriageCompleted(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	store.seen["fp-done"] = &Result{ID: "old", Fingerprint: "fp-done", Status: StatusComplete}
	store.results["old"] = store.seen["fp-done"]

	provider := &mockProvider{
		responses: []*LLMResponse{{
			Content:    []ContentBlock{{Type: "text", Text: "re-analysis"}},
			StopReason: StopEnd,
			Usage:      Usage{InputTokens: 10, OutputTokens: 5},
		}},
	}
	engine := NewEngine(provider, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider())
	svc := NewService(store, engine, log.Nop(), nil, nil, noop.NewTracerProvider())

	sr, err := svc.Submit(context.Background(), &alert.Alert{
		Status:      "firing",
		Fingerprint: "fp-done",
		Labels:      map[string]string{"alertname": "Retriage"},
		Annotations: map[string]string{},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sr.Skipped {
		t.Error("expected completed fingerprint to allow retriage")
	}
	if sr.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestSubmit_StoreError(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	store.getErr = errors.New("db down")

	svc := NewService(store, NewEngine(&mockProvider{}, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider()), log.Nop(), nil, nil, noop.NewTracerProvider())

	_, err := svc.Submit(context.Background(), &alert.Alert{
		Status:      "firing",
		Fingerprint: "fp-err",
		Labels:      map[string]string{"alertname": "Test"},
	})
	if err == nil {
		t.Fatal("expected error from store")
	}
}

func TestGet_Passthrough(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	want := &Result{ID: "t-1", Fingerprint: "fp-1", Status: StatusComplete}
	store.results["t-1"] = want

	svc := NewService(store, NewEngine(&mockProvider{}, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider()), log.Nop(), nil, nil, noop.NewTracerProvider())

	got, ok, err := svc.Get(context.Background(), "t-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected result to be found")
	}
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	svc := NewService(store, NewEngine(&mockProvider{}, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider()), log.Nop(), nil, nil, noop.NewTracerProvider())

	_, ok, err := svc.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("expected ok=false for missing ID")
	}
}

func TestSubmit_AsyncTriageCompletes(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	provider := &mockProvider{
		responses: []*LLMResponse{{
			Content:    []ContentBlock{{Type: "text", Text: "done analyzing"}},
			StopReason: StopEnd,
			Usage:      Usage{InputTokens: 100, OutputTokens: 50},
		}},
	}
	engine := NewEngine(provider, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider())
	svc := NewService(store, engine, log.Nop(), nil, nil, noop.NewTracerProvider())

	sr, err := svc.Submit(context.Background(), &alert.Alert{
		Status:      "firing",
		Fingerprint: "fp-async",
		Labels:      map[string]string{"alertname": "AsyncTest"},
		Annotations: map[string]string{"summary": "test"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for async triage to complete. Read only through the store to avoid
	// data races with the goroutine mutating the result.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, ok, _ := store.Get(context.Background(), sr.ID)
		if ok && (r.Status == StatusComplete || r.Status == StatusFailed) {
			if r.Analysis != "done analyzing" {
				t.Errorf("analysis = %q, want %q", r.Analysis, "done analyzing")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("triage did not complete within deadline")
}

func TestSubmit_NotifiesOnCompletion(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	notifier := newMockNotifier()
	provider := &mockProvider{
		responses: []*LLMResponse{{
			Content:    []ContentBlock{{Type: "text", Text: "notified analysis"}},
			StopReason: StopEnd,
			Usage:      Usage{InputTokens: 100, OutputTokens: 50},
		}},
	}
	engine := NewEngine(provider, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider())
	svc := NewService(store, engine, log.Nop(), nil, notifier, noop.NewTracerProvider())

	sr, err := svc.Submit(context.Background(), &alert.Alert{
		Status:      "firing",
		Fingerprint: "fp-notify",
		Labels:      map[string]string{"alertname": "NotifyTest"},
		Annotations: map[string]string{"summary": "test"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case <-notifier.called:
	case <-time.After(2 * time.Second):
		t.Fatal("notifier was not called within deadline")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()

	if notifier.calls != 1 {
		t.Errorf("notifier calls = %d, want 1", notifier.calls)
	}
	if notifier.last == nil {
		t.Fatal("expected notifier to receive result")
	}
	if notifier.last.ID != sr.ID {
		t.Errorf("notifier result ID = %q, want %q", notifier.last.ID, sr.ID)
	}
	if notifier.last.Analysis != "notified analysis" {
		t.Errorf("notifier result analysis = %q, want %q", notifier.last.Analysis, "notified analysis")
	}
}

func TestSubmit_NotifierErrorDoesNotFail(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	notifier := newMockNotifier()
	notifier.err = errors.New("webhook down")

	var completed atomic.Bool
	provider := &mockProvider{
		responses: []*LLMResponse{{
			Content:    []ContentBlock{{Type: "text", Text: "analysis despite notify error"}},
			StopReason: StopEnd,
			Usage:      Usage{InputTokens: 100, OutputTokens: 50},
		}},
	}
	engine := NewEngine(provider, nil, log.Nop(), EngineHooks{}, noop.NewTracerProvider())
	svc := NewService(store, engine, log.Nop(), nil, notifier, noop.NewTracerProvider())

	sr, err := svc.Submit(context.Background(), &alert.Alert{
		Status:      "firing",
		Fingerprint: "fp-notify-err",
		Labels:      map[string]string{"alertname": "NotifyErrTest"},
		Annotations: map[string]string{"summary": "test"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for triage to complete by polling the store.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, ok, _ := store.Get(context.Background(), sr.ID)
		if ok && (r.Status == StatusComplete || r.Status == StatusFailed) {
			completed.Store(true)
			if r.Status != StatusComplete {
				t.Errorf("status = %q, want %q", r.Status, StatusComplete)
			}
			if r.Analysis != "analysis despite notify error" {
				t.Errorf("analysis = %q, want %q", r.Analysis, "analysis despite notify error")
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !completed.Load() {
		t.Fatal("triage did not complete within deadline")
	}
}
