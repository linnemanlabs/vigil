// internal/triage/llm.go
package triage

import (
	"context"
	"encoding/json"

	"github.com/linnemanlabs/vigil/internal/tools"
)

// Provider is the interface for any LLM backend.
type Provider interface {
	Send(ctx context.Context, req *LLMRequest) (*LLMResponse, error)
}

// LLMRequest represents the input to the LLM provider, including the conversation history and available tools.
type LLMRequest struct {
	MaxTokens int
	System    string
	Messages  []Message
	Tools     []tools.ToolDef
}

// LLMResponse represents the output from the LLM provider, including the generated content, stop reason, and token usage.
type LLMResponse struct {
	Content    []ContentBlock
	StopReason StopReason
	Usage      Usage
}

// StopReason indicates why the LLM stopped generating content, such as reaching the end of the response or requesting a tool call.
type StopReason string

const (
	StopEnd     StopReason = "end_turn"
	StopToolUse StopReason = "tool_use"
)

// Message represents a single message in the conversation, which can be from the user or the assistant, and can contain either text or tool calls.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
