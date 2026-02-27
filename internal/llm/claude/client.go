package claude

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/linnemanlabs/vigil/internal/tools"
	"github.com/linnemanlabs/vigil/internal/triage"
)

// Client is a wrapper around the Anthropic SDK client that implements our internal triage.Provider interface,
// allowing us to send requests to the Claude API and receive responses in our internal format.
type Client struct {
	client anthropic.Client
	model  anthropic.Model
}

// New creates a new Claude API client with the given API key and model name.
func New(apiKey, model string) *Client {
	return &Client{
		model:  anthropic.Model(model),
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
	}
}

// Send sends a request to the Claude API, converting from our internal LLMRequest format to the SDK's expected format,
// and then converts the response back to our internal LLMResponse format. It handles any errors that occur during the API call.
func (c *Client) Send(ctx context.Context, req *triage.LLMRequest) (*triage.LLMResponse, error) {
	params := anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: int64(req.MaxTokens),
		System: []anthropic.TextBlockParam{
			{Text: req.System},
		},
		Messages: toSDKMessages(req.Messages),
		Tools:    toSDKTools(req.Tools),
	}

	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("claude api: %w", err)
	}
	return fromSDKResponse(resp), nil
}

func toSDKMessages(msgs []triage.Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, len(msgs))
	for i, m := range msgs {
		blocks := make([]anthropic.ContentBlockParamUnion, len(m.Content))
		for j := range m.Content {
			switch m.Content[j].Type {
			case "text":
				blocks[j] = anthropic.ContentBlockParamUnion{
					OfText: &anthropic.TextBlockParam{Text: m.Content[j].Text},
				}
			case "tool_use":
				blocks[j] = anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    m.Content[j].ID,
						Name:  m.Content[j].Name,
						Input: m.Content[j].Input,
					},
				}
			case "tool_result":
				blocks[j] = anthropic.ContentBlockParamUnion{
					OfToolResult: &anthropic.ToolResultBlockParam{
						ToolUseID: m.Content[j].ToolUseID,
						Content: []anthropic.ToolResultBlockParamContentUnion{
							{OfText: &anthropic.TextBlockParam{Text: m.Content[j].Content}},
						},
						IsError: anthropic.Bool(m.Content[j].IsError),
					},
				}
			}
		}
		out[i] = anthropic.MessageParam{
			Role:    anthropic.MessageParamRole(m.Role),
			Content: blocks,
		}
	}
	return out
}

func toSDKTools(defs []tools.ToolDef) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, len(defs))
	for i, d := range defs {
		// parse our JSON schema into the SDK's expected structure
		var schema anthropic.ToolInputSchemaParam
		_ = json.Unmarshal(d.InputSchema, &schema)

		out[i] = anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        d.Name,
				Description: anthropic.String(d.Description),
				InputSchema: schema,
			},
		}
	}
	return out
}

func fromSDKResponse(r *anthropic.Message) *triage.LLMResponse {
	blocks := make([]triage.ContentBlock, len(r.Content))

	for i := range r.Content {
		b := &r.Content[i]
		switch b.Type {
		case "text":
			blocks[i] = triage.ContentBlock{
				Type: "text",
				Text: b.Text,
			}
		case "tool_use":
			blocks[i] = triage.ContentBlock{
				Type:  "tool_use",
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Input,
			}
		}
	}

	var stopReason triage.StopReason
	switch r.StopReason {
	case anthropic.StopReasonEndTurn:
		stopReason = triage.StopEnd
	case anthropic.StopReasonToolUse:
		stopReason = triage.StopToolUse
	case anthropic.StopReasonMaxTokens:
		stopReason = triage.StopMaxTokens
	case anthropic.StopReasonStopSequence:
		stopReason = triage.StopStopSequence
	case anthropic.StopReasonPauseTurn:
		stopReason = triage.StopPauseTurn
	case anthropic.StopReasonRefusal:
		stopReason = triage.StopRefusal
	default:
		stopReason = triage.StopReason(r.StopReason)
	}

	return &triage.LLMResponse{
		Content:    blocks,
		StopReason: stopReason,
		Usage: triage.Usage{
			InputTokens:  int(r.Usage.InputTokens),
			OutputTokens: int(r.Usage.OutputTokens),
		},
		Model: string(r.Model),
	}
}
