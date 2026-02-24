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

func (m *mockTool) Name() string                                                          { return m.name }
func (m *mockTool) Description() string                                                    { return "mock tool" }
func (m *mockTool) Parameters() json.RawMessage                                            { return json.RawMessage(`{"type":"object"}`) }
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

	registry := tools.NewRegistry()
	provider := &mockProvider{
		responses: []*LLMResponse{{
			Content:    []ContentBlock{{Type: "text", Text: "analysis: all good"}},
			StopReason: StopEnd,
			Usage:      Usage{InputTokens: 100, OutputTokens: 50},
		}},
	}
	engine := NewEngine(provider, registry, log.Nop())

	rr := engine.Run(context.Background(), testAlert())

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.Analysis != "analysis: all good" {
		t.Errorf("analysis = %q, want %q", rr.Analysis, "analysis: all good")
	}
	if rr.TokensUsed != 150 {
		t.Errorf("tokens = %d, want 150", rr.TokensUsed)
	}
	if rr.Duration <= 0 {
		t.Error("expected positive duration")
	}
	if rr.Conversation == nil || len(rr.Conversation.Turns) == 0 {
		t.Fatal("expected conversation turns")
	}
	if rr.Conversation.Turns[0].Role != "assistant" {
		t.Errorf("first turn role = %q, want assistant", rr.Conversation.Turns[0].Role)
	}
	if rr.Conversation.Turns[0].Usage == nil {
		t.Error("expected usage on assistant turn")
	}
}

func TestRun_ToolUseLoop(t *testing.T) {
	t.Parallel()

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
	engine := NewEngine(provider, registry, log.Nop())

	rr := engine.Run(context.Background(), testAlert())

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.Analysis != "tool says 42" {
		t.Errorf("analysis = %q, want %q", rr.Analysis, "tool says 42")
	}
	if rr.ToolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", rr.ToolCalls)
	}
	if rr.TokensUsed != 450 {
		t.Errorf("tokens = %d, want 450", rr.TokensUsed)
	}
	// Should have 3 turns: assistant (tool_use), user (tool_result), assistant (final)
	if len(rr.Conversation.Turns) != 3 {
		t.Errorf("conversation turns = %d, want 3", len(rr.Conversation.Turns))
	}
}

func TestRun_UnknownTool(t *testing.T) {
	t.Parallel()

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
	engine := NewEngine(provider, registry, log.Nop())

	rr := engine.Run(context.Background(), testAlert())

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.Analysis != "recovered from unknown tool" {
		t.Errorf("analysis = %q, want %q", rr.Analysis, "recovered from unknown tool")
	}
}

func TestRun_ToolExecutionError(t *testing.T) {
	t.Parallel()

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
	engine := NewEngine(provider, registry, log.Nop())

	rr := engine.Run(context.Background(), testAlert())

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.ToolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", rr.ToolCalls)
	}
}

func TestRun_LLMError(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	provider := &mockProvider{
		errs: []error{errors.New("api key expired")},
	}
	engine := NewEngine(provider, registry, log.Nop())

	rr := engine.Run(context.Background(), testAlert())

	if rr.Status != StatusFailed {
		t.Errorf("status = %q, want %q", rr.Status, StatusFailed)
	}
	if !strings.Contains(rr.Analysis, "api key expired") {
		t.Errorf("analysis = %q, want it to contain the error", rr.Analysis)
	}
}

func TestRun_MaxToolRoundsLimit(t *testing.T) {
	t.Parallel()

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
	engine := NewEngine(provider, registry, log.Nop())

	rr := engine.Run(context.Background(), testAlert())

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if !strings.Contains(rr.Analysis, "tool call budget") {
		t.Errorf("analysis = %q, want it to mention tool call budget", rr.Analysis)
	}
	if rr.ToolCalls != MaxToolRounds {
		t.Errorf("tool_calls = %d, want %d", rr.ToolCalls, MaxToolRounds)
	}
}

func TestRun_MaxTokensLimit(t *testing.T) {
	t.Parallel()

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
	engine := NewEngine(provider, registry, log.Nop())

	rr := engine.Run(context.Background(), testAlert())

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if !strings.Contains(rr.Analysis, "token budget") {
		t.Errorf("analysis = %q, want it to mention token budget", rr.Analysis)
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
