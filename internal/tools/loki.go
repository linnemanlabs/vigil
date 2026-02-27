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

type lokiInput struct {
	Query string `json:"query"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type logLine struct {
	Timestamp string            `json:"ts"`
	Line      string            `json:"line"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string       `json:"resultType"`
		Result     []lokiStream `json:"result"`
	} `json:"data"`
}

func flattenStreams(results []lokiStream, limit int) []logLine {
	lines := make([]logLine, 0, limit)

	for _, stream := range results {
		includeLabels := true
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
				includeLabels = false
			}
			lines = append(lines, ll)
			if len(lines) >= limit {
				return lines
			}
		}
	}
	return lines
}

func parseLokiInput(params json.RawMessage) (lokiInput, error) {
	var input lokiInput
	if err := json.Unmarshal(params, &input); err != nil {
		return input, fmt.Errorf("invalid params: %w", err)
	}
	if input.Query == "" {
		return input, fmt.Errorf("query is required")
	}

	switch {
	case input.Limit <= 0:
		input.Limit = 100
	case input.Limit > 500:
		input.Limit = 500
	}

	now := time.Now().UTC()
	if input.Start == "" {
		input.Start = now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	}
	if input.End == "" {
		input.End = now.Format(time.RFC3339Nano)
	}

	// Cap range to 6 hours
	startTime, _ := time.Parse(time.RFC3339, input.Start)
	endTime, _ := time.Parse(time.RFC3339, input.End)
	if endTime.Sub(startTime) > 6*time.Hour {
		input.Start = endTime.Add(-6 * time.Hour).Format(time.RFC3339Nano)
	}

	return input, nil
}

// NewLokiQuery creates a new Loki query tool with the given endpoint and tenant ID.
func NewLokiQuery(endpoint, tenantID string) *LokiQuery {
	return &LokiQuery{
		endpoint:   endpoint,
		tenantID:   tenantID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

const successStatus = "success"

// Name returns the unique name of the tool, which is used to identify it when the LLM wants to call it.
func (l *LokiQuery) Name() string { return "query_logs" }

// Description returns an llm-friendly description of what the Loki query tool does and when to use it.
func (l *LokiQuery) Description() string {
	return `Query Loki for log entries using LogQL. Use this to search for logs from specific hosts, 
services, or time ranges. Useful for investigating errors, checking what happened before or during 
an alert, and finding relevant log lines that explain the root cause.

Common label selectors: {node="hostname"}, {job="systemd-journal"}, {service_name="myservice"}
You can add line filters: {node="hostname"} |= "error" or {node="hostname"} |~ "OOM|killed"
Use limit parameter to control how many log lines are returned.
Maximum query range is 6 hours per query, data retention is 1 year in total. For longer investigations, make multiple queries with different time windows.

Prefer exact string matches (|= "exact") over regex (|~) when possible, as regex is much slower.
Avoid short common substrings in regex alternations (e.g. "log", "tmp", "clean") as they match too broadly and cause timeouts.
Use specific terms: |= "logrotate" is fast, |~ "log|tmp|clean" is slow.
When searching for multiple terms, prefer multiple sequential queries with |= over one regex with many alternations.
`
}

// Parameters returns the JSON schema for the input parameters required to execute a Loki query.
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

// Execute performs the Loki query based on the provided parameters, handling HTTP communication and response parsing.
func (l *LokiQuery) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	input, err := parseLokiInput(params)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if input.Start == "" {
		input.Start = now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	}
	if input.End == "" {
		input.End = now.Format(time.RFC3339Nano)
	}

	// Cap the query range to 6 hours to prevent excessively large queries.
	startTime, _ := time.Parse(time.RFC3339, input.Start)
	endTime, _ := time.Parse(time.RFC3339, input.End)
	if endTime.Sub(startTime) > 6*time.Hour {
		startTime = endTime.Add(-6 * time.Hour)
		input.Start = startTime.Format(time.RFC3339Nano)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if l.tenantID != "" {
		req.Header.Set("X-Scope-OrgID", l.tenantID)
	}

	resp, err := l.httpClient.Do(req) //nolint:gosec // G704 - endpoint is set at construction from config, not from tool params.
	// LLM-controlled inputs (query, start, end, limit) are query-string encoded via url.Values.Set().
	if err != nil {
		return nil, fmt.Errorf("loki query failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MB
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki returned %d: %s", resp.StatusCode, string(body))
	}

	var lokiResp lokiResponse
	if err := json.Unmarshal(body, &lokiResp); err != nil {
		return body, nil
	}
	if lokiResp.Status != successStatus {
		return nil, fmt.Errorf("loki query failed: %s", string(body))
	}

	lines := flattenStreams(lokiResp.Data.Result, input.Limit)

	output := map[string]any{
		"stream_count": len(lokiResp.Data.Result),
		"line_count":   len(lines),
		"lines":        lines,
		"truncated":    len(lines) >= input.Limit,
	}
	return json.Marshal(output)
}
