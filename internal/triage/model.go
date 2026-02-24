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

	// StatusFailed means finished with errors
	StatusFailed Status = "failed"
)

// Result is the outcome of a triage run.
type Result struct {
	ID           string        `json:"id"`
	Fingerprint  string        `json:"fingerprint"`
	Status       Status        `json:"status"`
	Alert        string        `json:"alert_name"`
	Severity     string        `json:"severity"`
	Summary      string        `json:"summary"`
	Analysis     string        `json:"analysis,omitempty"`
	Actions      []string      `json:"actions,omitempty"`
	Conversation *Conversation `json:"conversation,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	CompletedAt  time.Time     `json:"completed_at,omitempty"`
	Duration     float64       `json:"duration_seconds,omitempty"`
	TokensUsed   int           `json:"tokens_used,omitempty"`
	ToolCalls    int           `json:"tool_calls,omitempty"`
}

// Conversation records the full LLM interaction during a triage run.
type Conversation struct {
	Turns []Turn `json:"turns"`
}

// Turn is a single exchange in the conversation (assistant response or tool results).
type Turn struct {
	Role      string         `json:"role"`
	Content   []ContentBlock `json:"content"`
	Timestamp time.Time      `json:"timestamp"`
	Usage     *Usage         `json:"usage,omitempty"`
}
