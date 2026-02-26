package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/vigil/internal/alert"
	"github.com/linnemanlabs/vigil/internal/tools"
)

const (
	MaxToolRounds  = 15
	MaxTokens      = 50000
	ResponseTokens = 4096
)

// RunResult is the outcome of a single Engine.Run invocation.
type RunResult struct {
	Status       Status
	Analysis     string
	Actions      []string
	Conversation *Conversation
	CompletedAt  time.Time
	Duration     float64
	TokensUsed   int
	ToolCalls    int
}

// EngineHooks provides optional callbacks for instrumenting engine operations.
// All fields are optional, nil callbacks are safely ignored.
type EngineHooks struct {
	OnLLMCall  func(inputTokens, outputTokens int, duration float64)
	OnToolCall func(name string, duration float64, inputBytes, outputBytes int, isError bool)
	OnComplete func(status Status, duration float64, tokens, toolCalls int)
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

func (h *EngineHooks) complete(status Status, dur float64, tokens, calls int) {
	if h.OnComplete != nil {
		h.OnComplete(status, dur, tokens, calls)
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
func (e *Engine) Run(ctx context.Context, al *alert.Alert, onTurn TurnCallback) *RunResult {
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
	var totalTokens int
	var totalToolCalls int

	for {
		if totalToolCalls >= MaxToolRounds {
			L.Warn(ctx, "triage hit tool call limit", "limit", MaxToolRounds)
			dur := time.Since(start).Seconds()
			e.hooks.complete(StatusComplete, dur, totalTokens, totalToolCalls)
			return &RunResult{
				Status:       StatusComplete,
				Analysis:     "Triage terminated: tool call budget exhausted",
				Conversation: conv,
				CompletedAt:  time.Now(),
				Duration:     dur,
				TokensUsed:   totalTokens,
				ToolCalls:    totalToolCalls,
			}
		}
		if totalTokens >= MaxTokens {
			L.Warn(ctx, "triage hit token limit", "limit", MaxTokens)
			dur := time.Since(start).Seconds()
			e.hooks.complete(StatusComplete, dur, totalTokens, totalToolCalls)
			return &RunResult{
				Status:       StatusComplete,
				Analysis:     "Triage terminated: token budget exhausted",
				Conversation: conv,
				CompletedAt:  time.Now(),
				Duration:     dur,
				TokensUsed:   totalTokens,
				ToolCalls:    totalToolCalls,
			}
		}

		var toolDefs []tools.ToolDef
		if e.registry != nil {
			toolDefs = e.registry.ToToolDefs()
		}

		// call LLM provider with current conversation
		llmStart := time.Now()
		resp, err := e.provider.Send(ctx, &LLMRequest{
			MaxTokens: ResponseTokens,
			System:    buildSystemPrompt(al),
			Messages:  messages,
			Tools:     toolDefs,
		})
		if err != nil {
			L.Error(ctx, err, "llm call failed")
			dur := time.Since(start).Seconds()
			e.hooks.complete(StatusFailed, dur, totalTokens, totalToolCalls)
			return &RunResult{
				Status:       StatusFailed,
				Analysis:     fmt.Sprintf("LLM error: %v", err),
				Conversation: conv,
				CompletedAt:  time.Now(),
				Duration:     dur,
				TokensUsed:   totalTokens,
				ToolCalls:    totalToolCalls,
			}
		}

		totalTokens += resp.Usage.InputTokens + resp.Usage.OutputTokens
		e.hooks.llmCall(resp.Usage.InputTokens, resp.Usage.OutputTokens, time.Since(llmStart).Seconds())

		L.Info(ctx, "llm response",
			"stop_reason", resp.StopReason,
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"total_tokens", totalTokens,
		)

		// record assistant turn
		conv.Turns = append(conv.Turns, Turn{
			Role:      "assistant",
			Content:   resp.Content,
			Timestamp: time.Now(),
			Usage:     &resp.Usage,
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
			e.hooks.complete(StatusComplete, dur, totalTokens, totalToolCalls)
			return &RunResult{
				Status:       StatusComplete,
				Analysis:     analysis,
				Conversation: conv,
				CompletedAt:  time.Now(),
				Duration:     dur,
				TokensUsed:   totalTokens,
				ToolCalls:    totalToolCalls,
			}
		}

		// handle tool calls
		if resp.StopReason == StopToolUse {
			toolResults, calls := e.executeToolCalls(ctx, L, resp.Content)
			totalToolCalls += calls

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

func (e *Engine) executeToolCalls(ctx context.Context, logger log.Logger, content []ContentBlock) (results []ContentBlock, calls int) {
	for i := range content {
		block := &content[i]
		if block.Type != "tool_use" {
			continue
		}

		calls++
		logger.Info(ctx, "executing tool", "tool", block.Name, "call_number", calls)

		tool, ok := e.registry.Get(block.Name)
		if !ok {
			e.hooks.toolCall(block.Name, 0, len(block.Input), 0, true)
			results = append(results, ContentBlock{
				Type:      "tool_result",
				ToolUseID: block.ID,
				Content:   fmt.Sprintf("unknown tool: %s", block.Name),
				IsError:   true,
			})
			continue
		}

		toolStart := time.Now()
		output, err := tool.Execute(ctx, block.Input)
		toolDur := time.Since(toolStart).Seconds()
		if err != nil {
			logger.Error(ctx, err, "tool execution failed", "tool", block.Name)
			e.hooks.toolCall(block.Name, toolDur, len(block.Input), 0, true)
			results = append(results, ContentBlock{
				Type:      "tool_result",
				ToolUseID: block.ID,
				Content:   fmt.Sprintf("tool error: %v", err),
				IsError:   true,
			})
			continue
		}

		e.hooks.toolCall(block.Name, toolDur, len(block.Input), len(output), false)
		results = append(results, ContentBlock{
			Type:      "tool_result",
			ToolUseID: block.ID,
			Content:   string(output),
		})
	}
	return results, calls
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
