package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/vigil/internal/alert"
	"github.com/linnemanlabs/vigil/internal/tools"
)

var tracer = otel.Tracer("github.com/linnemanlabs/vigil/internal/triage")

const (
	MaxToolRounds  = 15
	MaxTokens      = 100000
	ResponseTokens = 4096
)

// RunResult is the outcome of a single Engine.Run invocation.
type RunResult struct {
	Status           Status
	Analysis         string
	ToolsUsed        []string
	Conversation     *Conversation
	CompletedAt      time.Time
	Duration         float64
	LLMTime          float64
	ToolTime         float64
	TokensUsed       int
	InputTokensUsed  int
	OutputTokensUsed int
	ToolCalls        int
	SystemPrompt     string
	Model            string
}

// CompleteEvent is passed to the OnComplete hook with per-triage aggregates.
type CompleteEvent struct {
	Status    Status
	Duration  float64
	LLMTime   float64
	ToolTime  float64
	Tokens    int
	ToolCalls int
	Model     string
}

// EngineHooks provides optional callbacks for instrumenting engine operations.
// All fields are optional, nil callbacks are safely ignored.
type EngineHooks struct {
	OnLLMCall  func(inputTokens, outputTokens int, duration float64)
	OnToolCall func(name string, duration float64, inputBytes, outputBytes int, isError bool)
	OnComplete func(CompleteEvent)
}

func (h *EngineHooks) llmCall(in, out int, dur float64) {
	if h.OnLLMCall != nil {
		h.OnLLMCall(in, out, dur)
	}
}

func (h *EngineHooks) toolCall(name string, dur float64, inBytes, outBytes int, isErr bool) {
	if h.OnToolCall != nil {
		h.OnToolCall(name, dur, inBytes, outBytes, isErr)
	}
}

func (h *EngineHooks) complete(e CompleteEvent) {
	if h.OnComplete != nil {
		h.OnComplete(e)
	}
}

// Engine provides the core triage logic, orchestrating interactions between
// the LLM provider and tool registry.
type Engine struct {
	provider Provider
	registry *tools.Registry
	logger   log.Logger
	hooks    EngineHooks
}

// NewEngine creates a new triage engine with the given dependencies.
func NewEngine(provider Provider, registry *tools.Registry, logger log.Logger, hooks EngineHooks) *Engine {
	return &Engine{
		provider: provider,
		registry: registry,
		logger:   logger,
		hooks:    hooks,
	}
}

// Run executes the triage process for a given alert. It returns a RunResult
// containing the outcome; the caller is responsible for persisting it.
// If onTurn is non-nil it is called after each turn is appended to the
// conversation; errors are logged but do not abort the triage loop.
func (e *Engine) Run(ctx context.Context, triageID string, al *alert.Alert, onTurn TurnCallback) *RunResult {
	start := time.Now()

	L := e.logger.With(
		"alert", al.Labels["alertname"],
		"fingerprint", al.Fingerprint,
	)

	messages := []Message{
		{Role: "user", Content: []ContentBlock{
			{Type: "text", Text: buildInitialPrompt(al)},
		}},
	}

	conv := &Conversation{}
	var totalInputTokens, totalOutputTokens int
	var totalToolCalls int
	var totalLLMTime, totalToolTime float64
	var lastModel string
	var chatSeq int
	toolsUsedSet := make(map[string]struct{})

	systemPrompt := buildSystemPrompt(al)

	for {
		if totalToolCalls >= MaxToolRounds {
			L.Warn(ctx, "triage hit tool call limit", "limit", MaxToolRounds)
			dur := time.Since(start).Seconds()
			e.hooks.complete(CompleteEvent{
				Status: StatusComplete, Duration: dur, LLMTime: totalLLMTime, ToolTime: totalToolTime,
				Tokens: totalInputTokens + totalOutputTokens, ToolCalls: totalToolCalls, Model: lastModel,
			})
			return &RunResult{
				Status:           StatusComplete,
				Analysis:         "Triage terminated: tool call budget exhausted",
				ToolsUsed:        sortedKeys(toolsUsedSet),
				Conversation:     conv,
				CompletedAt:      time.Now(),
				Duration:         dur,
				LLMTime:          totalLLMTime,
				ToolTime:         totalToolTime,
				TokensUsed:       totalInputTokens + totalOutputTokens,
				InputTokensUsed:  totalInputTokens,
				OutputTokensUsed: totalOutputTokens,
				ToolCalls:        totalToolCalls,
				SystemPrompt:     systemPrompt,
				Model:            lastModel,
			}
		}
		if totalInputTokens+totalOutputTokens >= MaxTokens {
			L.Warn(ctx, "triage hit token limit", "limit", MaxTokens)
			dur := time.Since(start).Seconds()
			e.hooks.complete(CompleteEvent{
				Status: StatusComplete, Duration: dur, LLMTime: totalLLMTime, ToolTime: totalToolTime,
				Tokens: totalInputTokens + totalOutputTokens, ToolCalls: totalToolCalls, Model: lastModel,
			})
			return &RunResult{
				Status:           StatusComplete,
				Analysis:         "Triage terminated: token budget exhausted",
				ToolsUsed:        sortedKeys(toolsUsedSet),
				Conversation:     conv,
				CompletedAt:      time.Now(),
				Duration:         dur,
				LLMTime:          totalLLMTime,
				ToolTime:         totalToolTime,
				TokensUsed:       totalInputTokens + totalOutputTokens,
				InputTokensUsed:  totalInputTokens,
				OutputTokensUsed: totalOutputTokens,
				ToolCalls:        totalToolCalls,
				SystemPrompt:     systemPrompt,
				Model:            lastModel,
			}
		}

		var toolDefs []tools.ToolDef
		if e.registry != nil {
			toolDefs = e.registry.ToToolDefs()
		}

		// call LLM provider with current conversation
		llmStart := time.Now()
		req := &LLMRequest{
			MaxTokens: ResponseTokens,
			System:    systemPrompt,
			Messages:  messages,
			Tools:     toolDefs,
		}
		llmCtx, llmSpan := tracer.Start(ctx, "llm.call", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "llm.call"),
			attribute.String("gen_ai.provider.name", "anthropic"),
			attribute.Int("gen_ai.request.max_tokens", ResponseTokens),
			attribute.String("vigil.triage.id", triageID),
			attribute.String("vigil.alert.fingerprint", al.Fingerprint),
			attribute.Int("vigil.chat.seq", chatSeq),
		))
		llmSpan.AddEvent("llm.request", trace.WithAttributes(
			attribute.String("llm.request.body", marshalMessages(req.Messages)),
		))
		resp, err := e.provider.Send(llmCtx, req)
		if err != nil {
			llmSpan.RecordError(err)
			llmSpan.SetStatus(codes.Error, err.Error())
			llmSpan.End()
			L.Error(ctx, err, "llm call failed")
			dur := time.Since(start).Seconds()
			e.hooks.complete(CompleteEvent{
				Status: StatusFailed, Duration: dur, LLMTime: totalLLMTime, ToolTime: totalToolTime,
				Tokens: totalInputTokens + totalOutputTokens, ToolCalls: totalToolCalls, Model: lastModel,
			})
			return &RunResult{
				Status:           StatusFailed,
				Analysis:         fmt.Sprintf("LLM error: %v", err),
				ToolsUsed:        sortedKeys(toolsUsedSet),
				Conversation:     conv,
				CompletedAt:      time.Now(),
				Duration:         dur,
				LLMTime:          totalLLMTime,
				ToolTime:         totalToolTime,
				TokensUsed:       totalInputTokens + totalOutputTokens,
				InputTokensUsed:  totalInputTokens,
				OutputTokensUsed: totalOutputTokens,
				ToolCalls:        totalToolCalls,
				SystemPrompt:     systemPrompt,
				Model:            lastModel,
			}
		}

		llmSpan.AddEvent("llm.response", trace.WithAttributes(
			attribute.String("llm.response.body", marshalContent(resp.Content)),
		))

		llmDur := time.Since(llmStart).Seconds()
		totalLLMTime += llmDur
		totalInputTokens += resp.Usage.InputTokens
		totalOutputTokens += resp.Usage.OutputTokens
		lastModel = resp.Model
		e.hooks.llmCall(resp.Usage.InputTokens, resp.Usage.OutputTokens, llmDur)

		llmSpan.SetAttributes(
			attribute.String("gen_ai.response.model", resp.Model),
			attribute.String("gen_ai.request.model", resp.Model),
			attribute.Int("gen_ai.usage.input_tokens", resp.Usage.InputTokens),
			attribute.Int("gen_ai.usage.output_tokens", resp.Usage.OutputTokens),
			attribute.StringSlice("gen_ai.response.finish_reasons", []string{string(resp.StopReason)}),
		)
		llmSpan.SetStatus(codes.Ok, "")
		llmSpan.End()
		chatSeq++

		L.Info(ctx, "llm response",
			"stop_reason", resp.StopReason,
			"duration", llmDur,
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"total_tokens", totalInputTokens+totalOutputTokens,
		)

		// record assistant turn
		conv.Turns = append(conv.Turns, Turn{
			Role:       "assistant",
			Content:    resp.Content,
			Timestamp:  time.Now(),
			Usage:      &resp.Usage,
			StopReason: string(resp.StopReason),
			Duration:   llmDur,
			Model:      resp.Model,
		})
		notifyTurn(ctx, L, onTurn, conv)

		// append assistant response to messages
		messages = append(messages, Message{
			Role:    "assistant",
			Content: resp.Content,
		})

		// done - extract final analysis
		if resp.StopReason == StopEnd {
			var analysis string
			for _, block := range resp.Content {
				if block.Type == "text" {
					analysis = block.Text
				}
			}
			dur := time.Since(start).Seconds()
			e.hooks.complete(CompleteEvent{
				Status: StatusComplete, Duration: dur, LLMTime: totalLLMTime, ToolTime: totalToolTime,
				Tokens: totalInputTokens + totalOutputTokens, ToolCalls: totalToolCalls, Model: lastModel,
			})
			return &RunResult{
				Status:           StatusComplete,
				Analysis:         analysis,
				ToolsUsed:        sortedKeys(toolsUsedSet),
				Conversation:     conv,
				CompletedAt:      time.Now(),
				Duration:         dur,
				LLMTime:          totalLLMTime,
				ToolTime:         totalToolTime,
				TokensUsed:       totalInputTokens + totalOutputTokens,
				InputTokensUsed:  totalInputTokens,
				OutputTokensUsed: totalOutputTokens,
				ToolCalls:        totalToolCalls,
				SystemPrompt:     systemPrompt,
				Model:            lastModel,
			}
		}

		// handle tool calls
		if resp.StopReason == StopToolUse {
			toolResults, calls, batchToolDur := e.executeToolCalls(ctx, L, resp.Content, toolsUsedSet, triageID, al.Fingerprint)
			totalToolCalls += calls
			totalToolTime += batchToolDur

			// record tool results turn
			conv.Turns = append(conv.Turns, Turn{
				Role:      "user",
				Content:   toolResults,
				Timestamp: time.Now(),
			})
			notifyTurn(ctx, L, onTurn, conv)

			// append tool results to conversation for next LLM turn
			messages = append(messages, Message{
				Role:    "user",
				Content: toolResults,
			})
		}
	}
}

func notifyTurn(ctx context.Context, logger log.Logger, onTurn TurnCallback, conv *Conversation) {
	if onTurn == nil {
		return
	}
	seq := len(conv.Turns) - 1
	if err := onTurn(ctx, seq, &conv.Turns[seq]); err != nil {
		logger.Warn(ctx, "turn callback failed", "seq", seq, "err", err)
	}
}

func (e *Engine) executeToolCalls(ctx context.Context, logger log.Logger, content []ContentBlock, seen map[string]struct{}, triageID, fingerprint string) (results []ContentBlock, calls int, totalDur float64) {
	for i := range content {
		block := &content[i]
		if block.Type != "tool_use" {
			continue
		}

		calls++
		seen[block.Name] = struct{}{}
		logger.Info(ctx, "executing tool", "tool", block.Name, "call_number", calls)

		tool, ok := e.registry.Get(block.Name)
		if !ok {
			_, toolSpan := tracer.Start(ctx, "tool.execute", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
				attribute.String("gen_ai.operation.name", "tool.execute"),
				attribute.String("gen_ai.tool.name", block.Name),
				attribute.String("gen_ai.tool.call.id", block.ID),
				attribute.Bool("vigil.tool.is_error", true),
				attribute.String("vigil.triage.id", triageID),
				attribute.String("vigil.alert.fingerprint", fingerprint),
				attribute.String("vigil.tool.input", truncateSpanField(string(block.Input), 1024)),
			))
			toolSpan.AddEvent("tool.request", trace.WithAttributes(
				attribute.String("tool.request.body", string(block.Input)),
			))
			toolSpan.AddEvent("tool.result", trace.WithAttributes(
				attribute.String("tool.result.body", fmt.Sprintf("unknown tool: %s", block.Name)),
			))
			toolSpan.SetStatus(codes.Error, "unknown tool")
			toolSpan.End()

			e.hooks.toolCall(block.Name, 0, len(block.Input), 0, true)
			results = append(results, ContentBlock{
				Type:      "tool_result",
				ToolUseID: block.ID,
				Content:   fmt.Sprintf("unknown tool: %s", block.Name),
				IsError:   true,
			})
			continue
		}

		toolCtx, toolSpan := tracer.Start(ctx, "tool.execute", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "tool.execute"),
			attribute.String("gen_ai.tool.name", block.Name),
			attribute.String("gen_ai.tool.call.id", block.ID),
			attribute.Int("vigil.tool.input_bytes", len(block.Input)),
			attribute.String("vigil.triage.id", triageID),
			attribute.String("vigil.alert.fingerprint", fingerprint),
			attribute.String("vigil.tool.input", truncateSpanField(string(block.Input), 1024)),
		))

		toolSpan.AddEvent("tool.request", trace.WithAttributes(
			attribute.String("tool.request.body", string(block.Input)),
		))

		toolStart := time.Now()
		output, err := tool.Execute(toolCtx, block.Input)
		toolDur := time.Since(toolStart).Seconds()

		toolSpan.SetAttributes(attribute.Float64("vigil.tool.duration_s", toolDur))

		totalDur += toolDur

		if err != nil {
			logger.Error(ctx, err, "tool execution failed", "tool", block.Name, "duration", toolDur)
			toolSpan.AddEvent("tool.result", trace.WithAttributes(
				attribute.String("tool.result.body", err.Error()),
			))
			toolSpan.SetAttributes(
				attribute.Int("vigil.tool.output_bytes", 0),
				attribute.Bool("vigil.tool.is_error", true),
			)
			toolSpan.RecordError(err)
			toolSpan.SetStatus(codes.Error, err.Error())
			toolSpan.End()

			e.hooks.toolCall(block.Name, toolDur, len(block.Input), 0, true)
			results = append(results, ContentBlock{
				Type:      "tool_result",
				ToolUseID: block.ID,
				Content:   fmt.Sprintf("tool error: %v", err),
				IsError:   true,
				Duration:  toolDur,
			})
			continue
		}

		toolSpan.AddEvent("tool.result", trace.WithAttributes(
			attribute.String("tool.result.body", string(output)),
		))
		toolSpan.SetAttributes(
			attribute.Int("vigil.tool.output_bytes", len(output)),
			attribute.Bool("vigil.tool.is_error", false),
		)
		toolSpan.SetStatus(codes.Ok, "")
		toolSpan.End()

		logger.Info(ctx, "tool complete", "tool", block.Name, "duration", toolDur)
		e.hooks.toolCall(block.Name, toolDur, len(block.Input), len(output), false)
		results = append(results, ContentBlock{
			Type:      "tool_result",
			ToolUseID: block.ID,
			Content:   string(output),
			Duration:  toolDur,
		})
	}
	return results, calls, totalDur
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func truncateSpanField(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func marshalMessages(msgs []Message) string {
	b, _ := json.Marshal(msgs)
	return string(b)
}

func marshalContent(blocks []ContentBlock) string {
	b, _ := json.Marshal(blocks)
	return string(b)
}

// buildSystemPrompt constructs the system prompt for the LLM.
func buildSystemPrompt(_ *alert.Alert) string {
	return `You are Vigil, an infrastructure triage AI. You analyze alerts and diagnose root causes.

You have access to tools that let you query metrics, read logs, and inspect infrastructure.
Use them to investigate the alert, then provide a concise analysis with:
1. What is happening
2. Likely root cause
3. Recommended actions
4. Severity assessment (is this urgent or can it wait?)

Be concise and operational. This goes to an engineer's Slack channel.`
}

// buildInitialPrompt constructs the initial user message for the LLM.
func buildInitialPrompt(al *alert.Alert) string {
	labels, _ := json.MarshalIndent(al.Labels, "", "  ")
	annotations, _ := json.MarshalIndent(al.Annotations, "", "  ")

	return fmt.Sprintf(`Alert firing: %s
Severity: %s
Status: %s
Started: %s

Labels:
%s

Annotations:
%s

Generator: %s

Please investigate this alert using the available tools and provide your analysis.`,
		al.Labels["alertname"],
		al.Labels["severity"],
		al.Status,
		al.StartsAt.Format(time.RFC3339),
		string(labels),
		string(annotations),
		al.GeneratorURL,
	)
}
