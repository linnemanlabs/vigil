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

// PrometheusQueryRange is a tool for executing Prometheus range queries, which return metric data over a specified time range.
type PrometheusQueryRange struct {
	endpoint   string
	tenantID   string
	httpClient *http.Client
}

// NewPrometheusQueryRange creates a new instance of the PrometheusQueryRange tool with the given API endpoint and tenant ID.
func NewPrometheusQueryRange(endpoint, tenantID string) *PrometheusQueryRange {
	return &PrometheusQueryRange{
		endpoint:   endpoint,
		tenantID:   tenantID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Name returns the unique name of the tool, which is used to identify it when the LLM wants to call it.
func (p *PrometheusQueryRange) Name() string { return "query_metrics_range" }

// Description returns a human-friendly description of what the Prometheus range query tool does and when to use it.
func (p *PrometheusQueryRange) Description() string {
	return `Query Prometheus/Mimir metrics over a time range using PromQL. Use this to see trends, 
check how a metric changed over time, and identify when problems started. Returns a series 
of timestamped values for each matching time series.`
}

// Parameters returns the JSON schema for the input parameters required to execute a Prometheus range query.
func (p *PrometheusQueryRange) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "PromQL query expression"
            },
            "start": {
                "type": "string",
                "description": "Range start time (RFC3339). Example: 2026-02-24T00:00:00Z"
            },
            "end": {
                "type": "string",
                "description": "Range end time (RFC3339). Omit for current time."
            },
            "step": {
                "type": "string",
                "description": "Query resolution step (e.g. 60s, 5m, 1h). Default 5m."
            }
        },
        "required": ["query", "start"]
    }`)
}

// Execute performs the Prometheus range query based on the provided parameters, handling HTTP communication and response parsing.
func (p *PrometheusQueryRange) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Query string `json:"query"`
		Start string `json:"start"`
		End   string `json:"end,omitempty"`
		Step  string `json:"step,omitempty"`
	}
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if input.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if input.Start == "" {
		return nil, fmt.Errorf("start is required")
	}

	u, err := url.Parse(p.endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint: %w", err)
	}
	u.Path = path.Join(u.Path, "api/v1/query_range")

	q := u.Query()
	q.Set("query", input.Query)
	q.Set("start", input.Start)

	if input.End != "" {
		q.Set("end", input.End)
	} else {
		q.Set("end", time.Now().UTC().Format(time.RFC3339))
	}

	if input.Step != "" {
		q.Set("step", input.Step)
	} else {
		q.Set("step", "300") // 5m default
	}

	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if p.tenantID != "" {
		req.Header.Set("X-Scope-OrgID", p.tenantID)
	}

	resp, err := p.httpClient.Do(req) //nolint:gosec // G704 - endpoint is set at construction from config, not from tool params.
	// LLM-controlled inputs (query, start, end, limit) are query-string encoded via url.Values.Set().
	if err != nil {
		return nil, fmt.Errorf("prometheus range query failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MB
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, string(body))
	}

	var promResp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &promResp); err != nil {
		return body, nil
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: %s", string(body))
	}

	results := promResp.Data.Result
	truncated := false
	if len(results) > 20 {
		results = results[:20]
		truncated = true
	}

	output := map[string]any{
		"result_type":  promResp.Data.ResultType,
		"result_count": len(promResp.Data.Result),
		"results":      results,
		"truncated":    truncated,
	}

	return json.Marshal(output)
}
