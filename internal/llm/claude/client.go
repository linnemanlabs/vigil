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

type Client struct {
	client anthropic.Client
	model  anthropic.Model
}

func New(apiKey, model string) *Client {
	return &Client{
		model:  anthropic.Model(model),
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
	}
}

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

	// debug: log outgoing request
	/*
		if reqJSON, err := json.MarshalIndent(params, "", "  "); err == nil {
			fmt.Fprintf(os.Stderr, "==> claude request:\n%s\n", reqJSON)
		}
	*/

	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("claude api: %w", err)
	}
	/*
		// debug: log response
		if respJSON, err := json.MarshalIndent(resp, "", "  "); err == nil {
			fmt.Fprintf(os.Stderr, "==> claude response:\n%s\n", respJSON)
		}
	*/
	return fromSDKResponse(resp), nil
}

func toSDKMessages(msgs []triage.Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, len(msgs))
	for i, m := range msgs {
		blocks := make([]anthropic.ContentBlockParamUnion, len(m.Content))
		for j, b := range m.Content {
			switch b.Type {
			case "text":
				blocks[j] = anthropic.ContentBlockParamUnion{
					OfText: &anthropic.TextBlockParam{Text: b.Text},
				}
			case "tool_use":
				blocks[j] = anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    b.ID,
						Name:  b.Name,
						Input: b.Input,
					},
				}
			case "tool_result":
				blocks[j] = anthropic.ContentBlockParamUnion{
					OfToolResult: &anthropic.ToolResultBlockParam{
						ToolUseID: b.ToolUseID,
						Content: []anthropic.ToolResultBlockParamContentUnion{
							{OfText: &anthropic.TextBlockParam{Text: b.Content}},
						},
						IsError: anthropic.Bool(b.IsError),
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
	for i, b := range r.Content {
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
