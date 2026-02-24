package triage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/vigil/internal/alert"
	"github.com/linnemanlabs/vigil/internal/tools"
)

// mockProvider returns preconfigured responses in sequence.
type mockProvider struct {
	mu        sync.Mutex
	responses []*LLMResponse
	errs      []error
	callIdx   int
}

func (m *mockProvider) Send(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.callIdx
	m.callIdx++

	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	// fallback: end turn
	return &LLMResponse{
		Content:    []ContentBlock{{Type: "text", Text: "fallback"}},
		StopReason: StopEnd,
		Usage:      Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

// mockTool returns preconfigured Execute results.
type mockTool struct {
	name   string
	output json.RawMessage
	err    error
}

func (m *mockTool) Name() string                                                        { return m.name }
func (m *mockTool) Description() string                                                  { return "mock tool" }
func (m *mockTool) Parameters() json.RawMessage                                          { return json.RawMessage(`{"type":"object"}`) }
func (m *mockTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return m.output, m.err }

func testAlert() *alert.Alert {
	return &alert.Alert{
		Status:      "firing",
		Fingerprint: "fp-test",
		Labels: map[string]string{
			"alertname": "TestAlert",
			"severity":  "critical",
		},
		Annotations: map[string]string{
			"summary": "test summary",
		},
	}
}

func TestRun_SingleTurn(t *testing.T) {
	t.Parallel()

	store := NewStore()
	registry := tools.NewRegistry()
	provider := &mockProvider{
		responses: []*LLMResponse{{
			Content:    []ContentBlock{{Type: "text", Text: "analysis: all good"}},
			StopReason: StopEnd,
			Usage:      Usage{InputTokens: 100, OutputTokens: 50},
		}},
	}
	engine := NewEngine(provider, registry, store, log.Nop())

	result := &Result{ID: "t-1", Fingerprint: "fp-1", Status: StatusPending}
	store.Put(result)

	engine.Run(context.Background(), result, testAlert())

	got, ok := store.Get("t-1")
	if !ok {
		t.Fatal("result not found in store")
	}
	if got.Status != StatusComplete {
		t.Errorf("status = %q, want %q", got.Status, StatusComplete)
	}
	if got.Analysis != "analysis: all good" {
		t.Errorf("analysis = %q, want %q", got.Analysis, "analysis: all good")
	}
	if got.TokensUsed != 150 {
		t.Errorf("tokens = %d, want 150", got.TokensUsed)
	}
	if got.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestRun_ToolUseLoop(t *testing.T) {
	t.Parallel()

	store := NewStore()
	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name:   "test_tool",
		output: json.RawMessage(`{"value":"42"}`),
	})

	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "call-1", Name: "test_tool", Input: json.RawMessage(`{"q":"test"}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Content:    []ContentBlock{{Type: "text", Text: "tool says 42"}},
				StopReason: StopEnd,
				Usage:      Usage{InputTokens: 200, OutputTokens: 100},
			},
		},
	}
	engine := NewEngine(provider, registry, store, log.Nop())

	result := &Result{ID: "t-2", Fingerprint: "fp-2", Status: StatusPending}
	store.Put(result)

	engine.Run(context.Background(), result, testAlert())

	got, _ := store.Get("t-2")
	if got.Status != StatusComplete {
		t.Errorf("status = %q, want %q", got.Status, StatusComplete)
	}
	if got.Analysis != "tool says 42" {
		t.Errorf("analysis = %q, want %q", got.Analysis, "tool says 42")
	}
	if got.ToolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", got.ToolCalls)
	}
	if got.TokensUsed != 450 {
		t.Errorf("tokens = %d, want 450", got.TokensUsed)
	}
}

func TestRun_UnknownTool(t *testing.T) {
	t.Parallel()

	store := NewStore()
	registry := tools.NewRegistry() // empty registry

	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "call-1", Name: "nonexistent_tool", Input: json.RawMessage(`{}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 50, OutputTokens: 30},
			},
			{
				Content:    []ContentBlock{{Type: "text", Text: "recovered from unknown tool"}},
				StopReason: StopEnd,
				Usage:      Usage{InputTokens: 100, OutputTokens: 60},
			},
		},
	}
	engine := NewEngine(provider, registry, store, log.Nop())

	result := &Result{ID: "t-3", Fingerprint: "fp-3", Status: StatusPending}
	store.Put(result)

	engine.Run(context.Background(), result, testAlert())

	got, _ := store.Get("t-3")
	if got.Status != StatusComplete {
		t.Errorf("status = %q, want %q", got.Status, StatusComplete)
	}
	if got.Analysis != "recovered from unknown tool" {
		t.Errorf("analysis = %q, want %q", got.Analysis, "recovered from unknown tool")
	}
}

func TestRun_ToolExecutionError(t *testing.T) {
	t.Parallel()

	store := NewStore()
	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name: "failing_tool",
		err:  errors.New("connection refused"),
	})

	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "call-1", Name: "failing_tool", Input: json.RawMessage(`{}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 50, OutputTokens: 30},
			},
			{
				Content:    []ContentBlock{{Type: "text", Text: "tool failed, but I can still analyze"}},
				StopReason: StopEnd,
				Usage:      Usage{InputTokens: 100, OutputTokens: 60},
			},
		},
	}
	engine := NewEngine(provider, registry, store, log.Nop())

	result := &Result{ID: "t-4", Fingerprint: "fp-4", Status: StatusPending}
	store.Put(result)

	engine.Run(context.Background(), result, testAlert())

	got, _ := store.Get("t-4")
	if got.Status != StatusComplete {
		t.Errorf("status = %q, want %q", got.Status, StatusComplete)
	}
	if got.ToolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", got.ToolCalls)
	}
}

func TestRun_LLMError(t *testing.T) {
	t.Parallel()

	store := NewStore()
	registry := tools.NewRegistry()
	provider := &mockProvider{
		errs: []error{errors.New("api key expired")},
	}
	engine := NewEngine(provider, registry, store, log.Nop())

	result := &Result{ID: "t-5", Fingerprint: "fp-5", Status: StatusPending}
	store.Put(result)

	engine.Run(context.Background(), result, testAlert())

	got, _ := store.Get("t-5")
	if got.Status != StatusFailed {
		t.Errorf("status = %q, want %q", got.Status, StatusFailed)
	}
	if !strings.Contains(got.Analysis, "api key expired") {
		t.Errorf("analysis = %q, want it to contain the error", got.Analysis)
	}
}

func TestRun_MaxToolRoundsLimit(t *testing.T) {
	t.Parallel()

	store := NewStore()
	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name:   "loop_tool",
		output: json.RawMessage(`"ok"`),
	})

	// Build MaxToolRounds responses, each triggering one tool call
	responses := make([]*LLMResponse, MaxToolRounds)
	for i := range MaxToolRounds {
		responses[i] = &LLMResponse{
			Content: []ContentBlock{
				{Type: "tool_use", ID: "call-" + strings.Repeat("x", i+1), Name: "loop_tool", Input: json.RawMessage(`{}`)},
			},
			StopReason: StopToolUse,
			Usage:      Usage{InputTokens: 10, OutputTokens: 5},
		}
	}

	provider := &mockProvider{responses: responses}
	engine := NewEngine(provider, registry, store, log.Nop())

	result := &Result{ID: "t-6", Fingerprint: "fp-6", Status: StatusPending}
	store.Put(result)

	engine.Run(context.Background(), result, testAlert())

	got, _ := store.Get("t-6")
	if got.Status != StatusComplete {
		t.Errorf("status = %q, want %q", got.Status, StatusComplete)
	}
	if !strings.Contains(got.Analysis, "tool call budget") {
		t.Errorf("analysis = %q, want it to mention tool call budget", got.Analysis)
	}
	if got.ToolCalls != MaxToolRounds {
		t.Errorf("tool_calls = %d, want %d", got.ToolCalls, MaxToolRounds)
	}
}

func TestRun_MaxTokensLimit(t *testing.T) {
	t.Parallel()

	store := NewStore()
	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name:   "token_tool",
		output: json.RawMessage(`"ok"`),
	})

	// Each call uses 30k tokens, so after 2 calls (60k) we exceed MaxTokens (50k)
	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "call-1", Name: "token_tool", Input: json.RawMessage(`{}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 15000, OutputTokens: 15000},
			},
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "call-2", Name: "token_tool", Input: json.RawMessage(`{}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 15000, OutputTokens: 15000},
			},
		},
	}
	engine := NewEngine(provider, registry, store, log.Nop())

	result := &Result{ID: "t-7", Fingerprint: "fp-7", Status: StatusPending}
	store.Put(result)

	engine.Run(context.Background(), result, testAlert())

	got, _ := store.Get("t-7")
	if got.Status != StatusComplete {
		t.Errorf("status = %q, want %q", got.Status, StatusComplete)
	}
	if !strings.Contains(got.Analysis, "token budget") {
		t.Errorf("analysis = %q, want it to mention token budget", got.Analysis)
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()

	prompt := buildSystemPrompt(testAlert())
	if prompt == "" {
		t.Fatal("expected non-empty system prompt")
	}
	if !strings.Contains(prompt, "Vigil") {
		t.Errorf("system prompt should mention Vigil")
	}
}

func TestBuildInitialPrompt(t *testing.T) {
	t.Parallel()

	al := testAlert()
	prompt := buildInitialPrompt(al)

	for _, want := range []string{"TestAlert", "critical", "firing", "test summary"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("initial prompt missing %q", want)
		}
	}
}
