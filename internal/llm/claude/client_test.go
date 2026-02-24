package claude

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/linnemanlabs/vigil/internal/tools"
	"github.com/linnemanlabs/vigil/internal/triage"
)

func TestToSDKMessages_TextBlock(t *testing.T) {
	t.Parallel()

	msgs := []triage.Message{{
		Role:    "user",
		Content: []triage.ContentBlock{{Type: "text", Text: "hello"}},
	}}

	result := toSDKMessages(msgs)

	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("role = %q, want %q", result[0].Role, "user")
	}
	if len(result[0].Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(result[0].Content))
	}
	if result[0].Content[0].OfText == nil {
		t.Fatal("expected OfText to be set")
	}
	if result[0].Content[0].OfText.Text != "hello" {
		t.Errorf("text = %q, want %q", result[0].Content[0].OfText.Text, "hello")
	}
}

func TestToSDKMessages_ToolUseBlock(t *testing.T) {
	t.Parallel()

	msgs := []triage.Message{{
		Role: "assistant",
		Content: []triage.ContentBlock{{
			Type:  "tool_use",
			ID:    "tu-1",
			Name:  "query_metrics",
			Input: json.RawMessage(`{"query":"up"}`),
		}},
	}}

	result := toSDKMessages(msgs)

	block := result[0].Content[0]
	if block.OfToolUse == nil {
		t.Fatal("expected OfToolUse to be set")
	}
	if block.OfToolUse.ID != "tu-1" {
		t.Errorf("ID = %q, want %q", block.OfToolUse.ID, "tu-1")
	}
	if block.OfToolUse.Name != "query_metrics" {
		t.Errorf("Name = %q, want %q", block.OfToolUse.Name, "query_metrics")
	}
}

func TestToSDKMessages_ToolResultBlock(t *testing.T) {
	t.Parallel()

	msgs := []triage.Message{{
		Role: "user",
		Content: []triage.ContentBlock{{
			Type:      "tool_result",
			ToolUseID: "tu-1",
			Content:   "tool error: connection refused",
			IsError:   true,
		}},
	}}

	result := toSDKMessages(msgs)

	block := result[0].Content[0]
	if block.OfToolResult == nil {
		t.Fatal("expected OfToolResult to be set")
	}
	if block.OfToolResult.ToolUseID != "tu-1" {
		t.Errorf("ToolUseID = %q, want %q", block.OfToolResult.ToolUseID, "tu-1")
	}
	if !block.OfToolResult.IsError.Valid() || !block.OfToolResult.IsError.Value {
		t.Error("expected IsError to be true")
	}
}

func TestToSDKMessages_MixedBlocks(t *testing.T) {
	t.Parallel()

	msgs := []triage.Message{{
		Role: "assistant",
		Content: []triage.ContentBlock{
			{Type: "text", Text: "let me check"},
			{Type: "tool_use", ID: "tu-2", Name: "query_metrics", Input: json.RawMessage(`{}`)},
		},
	}}

	result := toSDKMessages(msgs)

	if len(result[0].Content) != 2 {
		t.Fatalf("content len = %d, want 2", len(result[0].Content))
	}
	if result[0].Content[0].OfText == nil {
		t.Error("first block should be text")
	}
	if result[0].Content[1].OfToolUse == nil {
		t.Error("second block should be tool_use")
	}
}

func TestToSDKTools(t *testing.T) {
	t.Parallel()

	defs := []tools.ToolDef{{
		Name:        "test_tool",
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	}}

	result := toSDKTools(defs)

	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if result[0].OfTool == nil {
		t.Fatal("expected OfTool to be set")
	}
	if result[0].OfTool.Name != "test_tool" {
		t.Errorf("name = %q, want %q", result[0].OfTool.Name, "test_tool")
	}
	if !result[0].OfTool.Description.Valid() || result[0].OfTool.Description.Value != "a test tool" {
		t.Errorf("description = %v, want %q", result[0].OfTool.Description, "a test tool")
	}
}

func TestFromSDKResponse_TextContent(t *testing.T) {
	t.Parallel()

	msg := &anthropic.Message{
		Content: []anthropic.ContentBlockUnion{
			{Type: "text", Text: "analysis result"},
		},
		StopReason: anthropic.StopReasonEndTurn,
		Usage:      anthropic.Usage{InputTokens: 100, OutputTokens: 50},
	}

	result := fromSDKResponse(msg)

	if len(result.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(result.Content))
	}
	if result.Content[0].Type != "text" {
		t.Errorf("type = %q, want %q", result.Content[0].Type, "text")
	}
	if result.Content[0].Text != "analysis result" {
		t.Errorf("text = %q, want %q", result.Content[0].Text, "analysis result")
	}
}

func TestFromSDKResponse_ToolUseContent(t *testing.T) {
	t.Parallel()

	msg := &anthropic.Message{
		Content: []anthropic.ContentBlockUnion{
			{
				Type:  "tool_use",
				ID:    "tu-99",
				Name:  "query_metrics",
				Input: json.RawMessage(`{"query":"up"}`),
			},
		},
		StopReason: anthropic.StopReasonToolUse,
		Usage:      anthropic.Usage{InputTokens: 200, OutputTokens: 100},
	}

	result := fromSDKResponse(msg)

	if len(result.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(result.Content))
	}
	if result.Content[0].Type != "tool_use" {
		t.Errorf("type = %q, want %q", result.Content[0].Type, "tool_use")
	}
	if result.Content[0].ID != "tu-99" {
		t.Errorf("id = %q, want %q", result.Content[0].ID, "tu-99")
	}
	if result.Content[0].Name != "query_metrics" {
		t.Errorf("name = %q, want %q", result.Content[0].Name, "query_metrics")
	}
}

func TestFromSDKResponse_StopReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sdk      anthropic.StopReason
		expected triage.StopReason
	}{
		{"end_turn", anthropic.StopReasonEndTurn, triage.StopEnd},
		{"tool_use", anthropic.StopReasonToolUse, triage.StopToolUse},
		{"unknown", anthropic.StopReason("max_tokens"), triage.StopReason("max_tokens")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := &anthropic.Message{
				StopReason: tt.sdk,
				Usage:      anthropic.Usage{},
			}
			result := fromSDKResponse(msg)
			if result.StopReason != tt.expected {
				t.Errorf("stop reason = %q, want %q", result.StopReason, tt.expected)
			}
		})
	}
}

func TestFromSDKResponse_Usage(t *testing.T) {
	t.Parallel()

	msg := &anthropic.Message{
		StopReason: anthropic.StopReasonEndTurn,
		Usage:      anthropic.Usage{InputTokens: 1234, OutputTokens: 567},
	}

	result := fromSDKResponse(msg)

	if result.Usage.InputTokens != 1234 {
		t.Errorf("input tokens = %d, want 1234", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 567 {
		t.Errorf("output tokens = %d, want 567", result.Usage.OutputTokens)
	}
}
