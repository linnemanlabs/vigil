package triage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

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

const claudeTestModel = "claude-sonnet-4-20250514"

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

func (m *mockTool) Name() string                { return m.name }
func (m *mockTool) Description() string         { return "mock tool" }
func (m *mockTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (m *mockTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return m.output, m.err
}

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
			Model:      claudeTestModel,
		}},
	}
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.Analysis != "analysis: all good" {
		t.Errorf("analysis = %q, want %q", rr.Analysis, "analysis: all good")
	}
	if rr.InputTokensUsed != 100 {
		t.Errorf("InputTokensUsed = %d, want 100", rr.InputTokensUsed)
	}
	if rr.OutputTokensUsed != 50 {
		t.Errorf("OutputTokensUsed = %d, want 50", rr.OutputTokensUsed)
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
	if rr.SystemPrompt == "" {
		t.Error("expected non-empty SystemPrompt")
	}
	if rr.Model != claudeTestModel {
		t.Errorf("model = %q, want %q", rr.Model, claudeTestModel)
	}
	turn := rr.Conversation.Turns[0]
	if turn.StopReason != string(StopEnd) {
		t.Errorf("turn stop_reason = %q, want %q", turn.StopReason, StopEnd)
	}
	if turn.Duration <= 0 {
		t.Error("expected positive turn duration")
	}
	if turn.Model != claudeTestModel {
		t.Errorf("turn model = %q, want %q", turn.Model, claudeTestModel)
	}
	if len(rr.ToolsUsed) != 0 {
		t.Errorf("ToolsUsed = %v, want empty", rr.ToolsUsed)
	}
	if rr.LLMTime <= 0 {
		t.Error("expected positive LLMTime")
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
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.Analysis != "tool says 42" {
		t.Errorf("analysis = %q, want %q", rr.Analysis, "tool says 42")
	}
	if rr.ToolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", rr.ToolCalls)
	}
	if rr.InputTokensUsed != 300 {
		t.Errorf("InputTokensUsed = %d, want 300", rr.InputTokensUsed)
	}
	if rr.OutputTokensUsed != 150 {
		t.Errorf("OutputTokensUsed = %d, want 150", rr.OutputTokensUsed)
	}
	// Should have 3 turns: assistant (tool_use), user (tool_result), assistant (final)
	if len(rr.Conversation.Turns) != 3 {
		t.Errorf("conversation turns = %d, want 3", len(rr.Conversation.Turns))
	}
	if len(rr.ToolsUsed) != 1 || rr.ToolsUsed[0] != "test_tool" {
		t.Errorf("ToolsUsed = %v, want [test_tool]", rr.ToolsUsed)
	}
	if rr.LLMTime <= 0 {
		t.Error("expected positive LLMTime")
	}
	if rr.ToolTime < 0 {
		t.Error("expected non-negative ToolTime")
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
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.Analysis != "recovered from unknown tool" {
		t.Errorf("analysis = %q, want %q", rr.Analysis, "recovered from unknown tool")
	}
	if len(rr.ToolsUsed) != 1 || rr.ToolsUsed[0] != "nonexistent_tool" {
		t.Errorf("ToolsUsed = %v, want [nonexistent_tool]", rr.ToolsUsed)
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
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.ToolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", rr.ToolCalls)
	}
	if len(rr.ToolsUsed) != 1 || rr.ToolsUsed[0] != "failing_tool" {
		t.Errorf("ToolsUsed = %v, want [failing_tool]", rr.ToolsUsed)
	}
}

func TestRun_LLMError(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	provider := &mockProvider{
		errs: []error{errors.New("api key expired")},
	}
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

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
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

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

	// Each call uses 60k tokens, so after 2 calls (120k) we exceed MaxTokens (100k)
	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "call-1", Name: "token_tool", Input: json.RawMessage(`{}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 30000, OutputTokens: 30000},
			},
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "call-2", Name: "token_tool", Input: json.RawMessage(`{}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 30000, OutputTokens: 30000},
			},
		},
	}
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

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

func TestRun_ObserverCalledPerTurn(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name:   "obs_tool",
		output: json.RawMessage(`{"v":"1"}`),
	})

	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "c-1", Name: "obs_tool", Input: json.RawMessage(`{}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 10, OutputTokens: 5},
			},
			{
				Content:    []ContentBlock{{Type: "text", Text: "done"}},
				StopReason: StopEnd,
				Usage:      Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	type observed struct {
		seq  int
		role string
	}
	var mu sync.Mutex
	var obs []observed

	cb := func(_ context.Context, seq int, turn *Turn) error {
		mu.Lock()
		defer mu.Unlock()
		obs = append(obs, observed{seq: seq, role: turn.Role})
		return nil
	}

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), cb)
	if rr.Status != StatusComplete {
		t.Fatalf("status = %q, want %q", rr.Status, StatusComplete)
	}

	// Expect 3 callbacks: assistant(0), user/tool_result(1), assistant(2)
	mu.Lock()
	defer mu.Unlock()
	if len(obs) != 3 {
		t.Fatalf("callback count = %d, want 3", len(obs))
	}
	wantRoles := []string{"assistant", "user", "assistant"}
	for i, want := range wantRoles {
		if obs[i].role != want {
			t.Errorf("obs[%d].role = %q, want %q", i, obs[i].role, want)
		}
		if obs[i].seq != i {
			t.Errorf("obs[%d].seq = %d, want %d", i, obs[i].seq, i)
		}
	}
}

func TestRun_ObserverErrorDoesNotAbort(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name:   "err_tool",
		output: json.RawMessage(`{"v":"1"}`),
	})

	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "c-1", Name: "err_tool", Input: json.RawMessage(`{}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 10, OutputTokens: 5},
			},
			{
				Content:    []ContentBlock{{Type: "text", Text: "completed"}},
				StopReason: StopEnd,
				Usage:      Usage{InputTokens: 10, OutputTokens: 5},
			},
		},
	}
	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})

	cb := func(_ context.Context, _ int, _ *Turn) error {
		return errors.New("callback boom")
	}

	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), cb)

	if rr.Status != StatusComplete {
		t.Errorf("status = %q, want %q", rr.Status, StatusComplete)
	}
	if rr.Analysis != "completed" {
		t.Errorf("analysis = %q, want %q", rr.Analysis, "completed")
	}
}

func TestRun_HooksCalled(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name:   "hook_tool",
		output: json.RawMessage(`{"result":"ok"}`),
	})

	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "c-1", Name: "hook_tool", Input: json.RawMessage(`{"q":"x"}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 100, OutputTokens: 50},
			},
			{
				Content:    []ContentBlock{{Type: "text", Text: "done"}},
				StopReason: StopEnd,
				Usage:      Usage{InputTokens: 200, OutputTokens: 80},
			},
		},
	}

	var (
		mu             sync.Mutex
		llmCalls       int
		totalTokensIn  int
		totalTokensOut int
		toolCalls      int
		lastToolName   string
		lastToolErr    bool
		completeCalls  int
		completeStatus Status
	)

	hooks := EngineHooks{
		OnLLMCall: func(in, out int, _ float64) {
			mu.Lock()
			defer mu.Unlock()
			llmCalls++
			totalTokensIn += in
			totalTokensOut += out
		},
		OnToolCall: func(name string, _ float64, _, _ int, isErr bool) {
			mu.Lock()
			defer mu.Unlock()
			toolCalls++
			lastToolName = name
			lastToolErr = isErr
		},
		OnComplete: func(e *CompleteEvent) {
			mu.Lock()
			defer mu.Unlock()
			completeCalls++
			completeStatus = e.Status
		},
	}

	engine := NewEngine(provider, registry, log.Nop(), hooks)
	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

	if rr.Status != StatusComplete {
		t.Fatalf("status = %q, want %q", rr.Status, StatusComplete)
	}

	mu.Lock()
	defer mu.Unlock()

	if llmCalls != 2 {
		t.Errorf("llm hook calls = %d, want 2", llmCalls)
	}
	if totalTokensIn != 300 {
		t.Errorf("total tokens in = %d, want 300", totalTokensIn)
	}
	if totalTokensOut != 130 {
		t.Errorf("total tokens out = %d, want 130", totalTokensOut)
	}
	if toolCalls != 1 {
		t.Errorf("tool hook calls = %d, want 1", toolCalls)
	}
	if lastToolName != "hook_tool" {
		t.Errorf("last tool name = %q, want %q", lastToolName, "hook_tool")
	}
	if lastToolErr {
		t.Error("expected tool error = false")
	}
	if completeCalls != 1 {
		t.Errorf("complete hook calls = %d, want 1", completeCalls)
	}
	if completeStatus != StatusComplete {
		t.Errorf("complete status = %q, want %q", completeStatus, StatusComplete)
	}
}

func TestRun_CreatesSpans(t *testing.T) { //nolint:gocognit // its a complex test and not worth the time to break down
	// Not parallel: swaps the global OTel tracer provider.

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prev)

	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name:   "span_tool",
		output: json.RawMessage(`{"ok":true}`),
	})

	provider := &mockProvider{
		responses: []*LLMResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "c-1", Name: "span_tool", Input: json.RawMessage(`{"q":"x"}`)},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 100, OutputTokens: 50},
				Model:      claudeTestModel,
			},
			{
				Content:    []ContentBlock{{Type: "text", Text: "done"}},
				StopReason: StopEnd,
				Usage:      Usage{InputTokens: 200, OutputTokens: 80},
				Model:      claudeTestModel,
			},
		},
	}

	engine := NewEngine(provider, registry, log.Nop(), EngineHooks{})
	rr := engine.Run(context.Background(), "test-triage-id", testAlert(), nil)

	if rr.Status != StatusComplete {
		t.Fatalf("status = %q, want %q", rr.Status, StatusComplete)
	}

	spans := exporter.GetSpans()

	// Count spans by name.
	counts := make(map[string]int)
	for _, s := range spans {
		counts[s.Name]++
	}

	if counts["llm.call"] != 2 {
		t.Errorf("llm.call spans = %d, want 2", counts["llm.call"])
	}
	if counts["tool.execute"] != 1 {
		t.Errorf("tool.execute spans = %d, want 1", counts["tool.execute"])
	}

	// Verify key attributes and events on llm.call spans.
	var chatSpanIdx int
	for _, s := range spans {
		if s.Name != "llm.call" {
			continue
		}
		attrs := make(map[string]any)
		for _, a := range s.Attributes {
			attrs[string(a.Key)] = a.Value.AsInterface()
		}
		if v, ok := attrs["gen_ai.operation.name"]; !ok || v != "llm.call" {
			t.Errorf("llm.call span missing gen_ai.operation.name=llm.call, got %v", v)
		}
		if v, ok := attrs["gen_ai.response.model"]; !ok || v != claudeTestModel {
			t.Errorf("llm.call span missing gen_ai.response.model, got %v", v)
		}
		if v, ok := attrs["vigil.triage.id"]; !ok || v != "test-triage-id" {
			t.Errorf("llm.call span vigil.triage.id = %v, want test-triage-id", v)
		}
		if v, ok := attrs["vigil.alert.fingerprint"]; !ok || v != "fp-test" {
			t.Errorf("llm.call span vigil.alert.fingerprint = %v, want fp-test", v)
		}
		if v, ok := attrs["vigil.chat.seq"]; !ok || v != int64(chatSpanIdx) {
			t.Errorf("llm.call span vigil.chat.seq = %v, want %d", v, chatSpanIdx)
		}

		// Verify llm.request and llm.response events.
		eventNames := make(map[string]bool)
		for _, ev := range s.Events {
			eventNames[ev.Name] = true
		}
		if !eventNames["llm.request"] {
			t.Errorf("llm.call span[%d] missing llm.request event", chatSpanIdx)
		}
		if !eventNames["llm.response"] {
			t.Errorf("llm.call span[%d] missing llm.response event", chatSpanIdx)
		}

		chatSpanIdx++
	}

	// Verify tool span attributes and events.
	for _, s := range spans {
		if s.Name != "tool.execute" {
			continue
		}
		attrs := make(map[string]any)
		for _, a := range s.Attributes {
			attrs[string(a.Key)] = a.Value.AsInterface()
		}
		if v, ok := attrs["gen_ai.operation.name"]; !ok || v != "tool.execute" {
			t.Errorf("tool span gen_ai.operation.name = %v, want tool.execute", v)
		}
		if v, ok := attrs["gen_ai.tool.name"]; !ok || v != "span_tool" {
			t.Errorf("tool span missing gen_ai.tool.name=span_tool, got %v", v)
		}
		if v, ok := attrs["vigil.tool.is_error"]; !ok || v != false {
			t.Errorf("tool span vigil.tool.is_error = %v, want false", v)
		}
		if v, ok := attrs["vigil.triage.id"]; !ok || v != "test-triage-id" {
			t.Errorf("tool span vigil.triage.id = %v, want test-triage-id", v)
		}
		if v, ok := attrs["vigil.tool.input"]; !ok || v != `{"q":"x"}` {
			t.Errorf("tool span vigil.tool.input = %v, want {\"q\":\"x\"}", v)
		}

		// Verify tool.request and tool.result events.
		eventNames := make(map[string]map[string]string)
		for _, ev := range s.Events {
			evAttrs := make(map[string]string)
			for _, a := range ev.Attributes {
				evAttrs[string(a.Key)] = a.Value.AsString()
			}
			eventNames[ev.Name] = evAttrs
		}
		if reqAttrs, ok := eventNames["tool.request"]; !ok {
			t.Error("tool.execute span missing tool.request event")
		} else if reqAttrs["tool.request.body"] != `{"q":"x"}` {
			t.Errorf("tool.request body = %q, want %q", reqAttrs["tool.request.body"], `{"q":"x"}`)
		}
		if resAttrs, ok := eventNames["tool.result"]; !ok {
			t.Error("tool.execute span missing tool.result event")
		} else if resAttrs["tool.result.body"] != `{"ok":true}` {
			t.Errorf("tool.result body = %q, want %q", resAttrs["tool.result.body"], `{"ok":true}`)
		}
		break
	}

	// Verify split token tracking.
	if rr.InputTokensUsed != 300 {
		t.Errorf("InputTokensUsed = %d, want 300", rr.InputTokensUsed)
	}
	if rr.OutputTokensUsed != 130 {
		t.Errorf("OutputTokensUsed = %d, want 130", rr.OutputTokensUsed)
	}
}
