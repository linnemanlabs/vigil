package triage

import "time"

// Status tracks where a triage is in its lifecycle.
type Status string

const (
	// StatusPending means created, not yet started
	StatusPending Status = "pending"

	// StatusInProgress means currently being processed
	StatusInProgress Status = "in_progress"

	// StatusComplete means finished successfully
	StatusComplete Status = "complete"

	// StatusFailed means finished with LLM provider errors
	StatusFailed Status = "failed"

	// StatusError means finished with infrastructure/store errors during orchestration
	StatusError Status = "error"

	// StatusMaxTurns means the triage hit the MaxToolRounds limit
	StatusMaxTurns Status = "max_turns"

	// StatusBudgetExceeded means the triage hit input or output token limits
	StatusBudgetExceeded Status = "budget_exceeded"
)

// IsTerminal reports whether the status represents a final state.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusComplete, StatusFailed, StatusError, StatusMaxTurns, StatusBudgetExceeded:
		return true
	case StatusPending, StatusInProgress:
		return false
	default:
		return false
	}
}

// Result is the outcome of a triage run.
type Result struct {
	ID           string        `json:"id"`
	Fingerprint  string        `json:"fingerprint"`
	Status       Status        `json:"status"`
	Alert        string        `json:"alert_name"`
	Severity     string        `json:"severity"`
	Summary      string        `json:"summary"`
	Analysis     string        `json:"analysis,omitempty"`
	ToolsUsed    []string      `json:"tools_used,omitempty"`
	Conversation *Conversation `json:"conversation,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	CompletedAt  time.Time     `json:"completed_at,omitempty"`
	Duration     float64       `json:"duration_seconds,omitempty"`
	LLMTime      float64       `json:"llm_time_seconds,omitempty"`
	ToolTime     float64       `json:"tool_time_seconds,omitempty"`
	TokensIn     int           `json:"tokens_in,omitempty"`
	TokensOut    int           `json:"tokens_out,omitempty"`
	ToolCalls    int           `json:"tool_calls,omitempty"`
	SystemPrompt string        `json:"system_prompt,omitempty"`
	Model        string        `json:"model,omitempty"`
}

// Conversation records the full LLM interaction during a triage run.
type Conversation struct {
	Turns []Turn `json:"turns"`
}

// Turn is a single exchange in the conversation (assistant response or tool results).
type Turn struct {
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	Timestamp  time.Time      `json:"timestamp"`
	Usage      *Usage         `json:"usage,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	Duration   float64        `json:"duration,omitempty"`
	Model      string         `json:"model,omitempty"`
}
