// internal/tools/loki.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"
)

// LokiQuery queries Loki for log entries matching a LogQL expression.
type LokiQuery struct {
	endpoint   string
	tenantID   string
	httpClient *http.Client
}

// NewLokiQuery creates a new Loki query tool with the given endpoint and tenant ID.
func NewLokiQuery(endpoint, tenantID string) *LokiQuery {
	return &LokiQuery{
		endpoint:   endpoint,
		tenantID:   tenantID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (l *LokiQuery) Name() string { return "query_logs" }

func (l *LokiQuery) Description() string {
	return `Query Loki for log entries using LogQL. Use this to search for logs from specific hosts, 
services, or time ranges. Useful for investigating errors, checking what happened before or during 
an alert, and finding relevant log lines that explain the root cause.

Common label selectors: {node="hostname"}, {job="systemd-journal"}, {service_name="myservice"}
You can add line filters: {node="hostname"} |= "error" or {node="hostname"} |~ "OOM|killed"
Use limit parameter to control how many log lines are returned.`
}

func (l *LokiQuery) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "LogQL query expression. Example: {node=\"jump-bastion-2a\"} |= \"error\""
            },
            "start": {
                "type": "string",
                "description": "Start time (RFC3339). Defaults to 1 hour ago."
            },
            "end": {
                "type": "string",
                "description": "End time (RFC3339). Defaults to now."
            },
            "limit": {
                "type": "integer",
                "description": "Maximum number of log lines to return. Default 100, max 500."
            }
        },
        "required": ["query"]
    }`)
}

func (l *LokiQuery) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Query string `json:"query"`
		Start string `json:"start,omitempty"`
		End   string `json:"end,omitempty"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if input.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if input.Limit <= 0 {
		input.Limit = 100
	}
	if input.Limit > 500 {
		input.Limit = 500
	}

	now := time.Now().UTC()
	if input.Start == "" {
		input.Start = now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	}
	if input.End == "" {
		input.End = now.Format(time.RFC3339Nano)
	}

	u, err := url.Parse(l.endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint: %w", err)
	}
	u.Path = path.Join(u.Path, "loki/api/v1/query_range")

	q := u.Query()
	q.Set("query", input.Query)
	q.Set("start", input.Start)
	q.Set("end", input.End)
	q.Set("limit", fmt.Sprintf("%d", input.Limit))
	q.Set("direction", "backward")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if l.tenantID != "" {
		req.Header.Set("X-Scope-OrgID", l.tenantID)
	}

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki query failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki returned %d: %s", resp.StatusCode, string(body))
	}

	var lokiResp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &lokiResp); err != nil {
		return body, nil
	}

	if lokiResp.Status != "success" {
		return nil, fmt.Errorf("loki query failed: %s", string(body))
	}

	// flatten to readable log lines with timestamps
	type logLine struct {
		Timestamp string            `json:"ts"`
		Line      string            `json:"line"`
		Labels    map[string]string `json:"labels,omitempty"`
	}

	var lines []logLine
	includeLabels := true

	for _, stream := range lokiResp.Data.Result {
		for _, entry := range stream.Values {
			if len(entry) < 2 {
				continue
			}

			ll := logLine{
				Timestamp: entry[0],
				Line:      entry[1],
			}
			if includeLabels {
				ll.Labels = stream.Stream
				includeLabels = false // only include labels on first line per stream
			}

			lines = append(lines, ll)

			if len(lines) >= input.Limit {
				break
			}
		}
		if len(lines) >= input.Limit {
			break
		}
	}

	output := map[string]any{
		"stream_count": len(lokiResp.Data.Result),
		"line_count":   len(lines),
		"lines":        lines,
		"truncated":    len(lines) >= input.Limit,
	}

	return json.Marshal(output)
}
