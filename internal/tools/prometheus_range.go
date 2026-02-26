// internal/tools/prometheus_range.go
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

type PrometheusQueryRange struct {
	endpoint   string
	tenantID   string
	httpClient *http.Client
}

func NewPrometheusQueryRange(endpoint, tenantID string) *PrometheusQueryRange {
	return &PrometheusQueryRange{
		endpoint:   endpoint,
		tenantID:   tenantID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *PrometheusQueryRange) Name() string { return "query_metrics_range" }

func (p *PrometheusQueryRange) Description() string {
	return `Query Prometheus/Mimir metrics over a time range using PromQL. Use this to see trends, 
check how a metric changed over time, and identify when problems started. Returns a series 
of timestamped values for each matching time series.`
}

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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if p.tenantID != "" {
		req.Header.Set("X-Scope-OrgID", p.tenantID)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus range query failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
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
