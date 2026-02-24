// internal/triage/engine.go
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

// Engine provides the core triage logic, orchestrating interactions between the LLM provider, tool registry, and triage store.
type Engine struct {
	provider Provider
	registry *tools.Registry
	store    *Store
	logger   log.Logger
}

// NewEngine creates a new triage engine with the given dependencies.
func NewEngine(provider Provider, registry *tools.Registry, store *Store, logger log.Logger) *Engine {
	return &Engine{
		provider: provider,
		registry: registry,
		store:    store,
		logger:   logger,
	}
}

// Run executes the triage process for a given alert, updating the result in the store as it progresses.
func (e *Engine) Run(ctx context.Context, result *Result, al *alert.Alert) {
	start := time.Now()
	result.Status = StatusInProgress
	e.store.Put(result)

	L := e.logger.With(
		"triage_id", result.ID,
		"alert", result.Alert,
		"fingerprint", result.Fingerprint,
	)

	messages := []Message{
		{Role: "user", Content: []ContentBlock{
			{Type: "text", Text: buildInitialPrompt(al)},
		}},
	}

	var totalTokens int
	var totalToolCalls int

	for {
		if totalToolCalls >= MaxToolRounds {
			L.Warn(ctx, "triage hit tool call limit", "limit", MaxToolRounds)
			result.Analysis = "Triage terminated: tool call budget exhausted"
			break
		}
		if totalTokens >= MaxTokens {
			L.Warn(ctx, "triage hit token limit", "limit", MaxTokens)
			result.Analysis = "Triage terminated: token budget exhausted"
			break
		}

		// call LLM provider with current conversation
		resp, err := e.provider.Send(ctx, &LLMRequest{
			MaxTokens: ResponseTokens,
			System:    buildSystemPrompt(al),
			Messages:  messages,
			Tools:     e.registry.ToToolDefs(),
		})
		if err != nil {
			L.Error(ctx, err, "llm call failed")
			result.Status = StatusFailed
			result.Analysis = fmt.Sprintf("LLM error: %v", err)
			e.store.Put(result)
			return
		}

		totalTokens += resp.Usage.InputTokens + resp.Usage.OutputTokens

		L.Info(ctx, "llm response",
			"stop_reason", resp.StopReason,
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"total_tokens", totalTokens,
		)

		// append assistant response
		messages = append(messages, Message{
			Role:    "assistant",
			Content: resp.Content,
		})

		// done - extract final analysis
		if resp.StopReason == StopEnd {
			for _, block := range resp.Content {
				if block.Type == "text" {
					result.Analysis = block.Text
				}
			}
			break
		}

		// handle tool calls
		if resp.StopReason == StopToolUse {
			var toolResults []ContentBlock

			for _, block := range resp.Content {
				if block.Type != "tool_use" {
					continue
				}

				totalToolCalls++
				L.Info(ctx, "executing tool",
					"tool", block.Name,
					"call_number", totalToolCalls,
				)

				// lookup tool
				tool, ok := e.registry.Get(block.Name)
				if !ok {
					toolResults = append(toolResults, ContentBlock{
						Type:      "tool_result",
						ToolUseID: block.ID,
						Content:   fmt.Sprintf("unknown tool: %s", block.Name),
						IsError:   true,
					})
					continue
				}

				// execute tool
				output, err := tool.Execute(ctx, block.Input)
				if err != nil {
					L.Error(ctx, err, "tool execution failed", "tool", block.Name)
					toolResults = append(toolResults, ContentBlock{
						Type:      "tool_result",
						ToolUseID: block.ID,
						Content:   fmt.Sprintf("tool error: %v", err),
						IsError:   true,
					})
					continue
				}

				// append tool results to content for next LLM turn
				toolResults = append(toolResults, ContentBlock{
					Type:      "tool_result",
					ToolUseID: block.ID,
					Content:   string(output),
				})
			}

			// append tool results to conversation for next LLM turn
			messages = append(messages, Message{
				Role:    "user",
				Content: toolResults,
			})
		}
	}

	result.Status = StatusComplete
	result.CompletedAt = time.Now()
	result.Duration = time.Since(start).Seconds()
	result.TokensUsed = totalTokens
	result.ToolCalls = totalToolCalls
	e.store.Put(result)

	L.Info(ctx, "triage complete",
		"duration", result.Duration,
		"tokens", totalTokens,
		"tool_calls", totalToolCalls,
	)
}

// buildSystemPrompt constructs the system prompt for the LLM, providing instructions on how to analyze the alert and use tools effectively.
func buildSystemPrompt(al *alert.Alert) string {
	return `You are Vigil, an infrastructure triage AI. You analyze alerts and diagnose root causes.

You have access to tools that let you query metrics, read logs, and inspect infrastructure.
Use them to investigate the alert, then provide a concise analysis with:
1. What is happening
2. Likely root cause
3. Recommended actions
4. Severity assessment (is this urgent or can it wait?)

Be concise and operational. This goes to an engineer's Slack channel.`
}

// buildInitialPrompt constructs the initial user message for the LLM, summarizing the alert details and asking for an investigation.
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
