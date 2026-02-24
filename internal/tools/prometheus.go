// internal/tools/prometheus.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type PrometheusQuery struct {
	endpoint   string
	httpClient *http.Client
}

func NewPrometheusQuery(endpoint string) *PrometheusQuery {
	return &PrometheusQuery{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (p *PrometheusQuery) Name() string { return "query_metrics" }

func (p *PrometheusQuery) Description() string {
	return `Query Prometheus/Mimir metrics using PromQL. Use this to investigate metric values, 
check current and historical resource usage, labels that carry metadata, and correlate alert conditions with raw data. 
Returns instant query results with labels and values.`
}

func (p *PrometheusQuery) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "PromQL query expression"
            },
            "time": {
                "type": "string",
                "description": "Evaluation timestamp (RFC3339). Omit for current time."
            }
        },
        "required": ["query"]
    }`)
}

func (p *PrometheusQuery) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Query string `json:"query"`
		Time  string `json:"time,omitempty"`
	}
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	if input.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	u, err := url.Parse(p.endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint: %w", err)
	}
	u.Path = "/api/v1/query"

	q := u.Query()
	q.Set("query", input.Query)
	if input.Time != "" {
		q.Set("time", input.Time)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus query failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, string(body))
	}

	// parse and slim down the response so we don't waste context
	var promResp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &promResp); err != nil {
		return body, nil // return raw if we can't parse
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: %s", string(body))
	}

	// cap results to avoid blowing context window
	results := promResp.Data.Result
	truncated := false
	if len(results) > 50 {
		results = results[:50]
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
