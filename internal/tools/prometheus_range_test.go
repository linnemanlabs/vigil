package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestPrometheusRange(t *testing.T, tenantID string, handler http.HandlerFunc) *PrometheusQueryRange {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewPrometheusQueryRange(srv.URL, tenantID)
}

func TestPrometheusRange_Success(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("query") != "up" {
			t.Errorf("query = %q, want %q", r.URL.Query().Get("query"), "up")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up"},"values":[[1234,"1"],[1235,"1"]]}]}}`)
	})

	out, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up","start":"2026-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if parsed["result_type"] != "matrix" {
		t.Errorf("result_type = %v, want %q", parsed["result_type"], "matrix")
	}
	if parsed["truncated"] != false {
		t.Errorf("truncated = %v, want false", parsed["truncated"])
	}
}

func TestPrometheusRange_EmptyQuery(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not have made HTTP request")
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"","start":"2026-01-01T00:00:00Z"}`))
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q, want it to mention 'required'", err.Error())
	}
}

func TestPrometheusRange_MissingStart(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not have made HTTP request")
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up"}`))
	if err == nil {
		t.Fatal("expected error for missing start")
	}
	if !strings.Contains(err.Error(), "start is required") {
		t.Errorf("error = %q, want it to mention 'start is required'", err.Error())
	}
}

func TestPrometheusRange_InvalidParams(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not have made HTTP request")
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid params")
	}
	if !strings.Contains(err.Error(), "invalid params") {
		t.Errorf("error = %q, want it to mention 'invalid params'", err.Error())
	}
}

func TestPrometheusRange_HTTPError(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "internal error")
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up","start":"2026-01-01T00:00:00Z"}`))
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status code", err.Error())
	}
}

func TestPrometheusRange_NonSuccessStatus(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"error","errorType":"bad_data","error":"parse error"}`)
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"bad{}","start":"2026-01-01T00:00:00Z"}`))
	if err == nil {
		t.Fatal("expected error for non-success prometheus status")
	}
	if !strings.Contains(err.Error(), "prometheus query failed") {
		t.Errorf("error = %q, want it to mention 'prometheus query failed'", err.Error())
	}
}

func TestPrometheusRange_UnparsableResponse(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "this is not json at all")
	})

	out, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up","start":"2026-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v (unparsable body should return raw)", err)
	}
	if !strings.Contains(string(out), "this is not json at all") {
		t.Errorf("output = %q, want raw body", string(out))
	}
}

func TestPrometheusRange_Truncation(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(w http.ResponseWriter, _ *http.Request) {
		results := make([]string, 0, 30)
		for i := range 30 {
			results = append(results, fmt.Sprintf(`{"metric":{"i":"%d"},"values":[[1234,"%d"]]}`, i, i))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[%s]}}`, strings.Join(results, ","))
	})

	out, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up","start":"2026-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if parsed["truncated"] != true {
		t.Errorf("truncated = %v, want true", parsed["truncated"])
	}
	if int(parsed["result_count"].(float64)) != 30 {
		t.Errorf("result_count = %v, want 30", parsed["result_count"])
	}
	results, ok := parsed["results"].([]any)
	if !ok {
		t.Fatalf("results is not an array: %T", parsed["results"])
	}
	if len(results) != 20 {
		t.Errorf("len(results) = %d, want 20", len(results))
	}
}

func TestPrometheusRange_DefaultStepAndEnd(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheusRange(t, "test", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("step"); got != "300" {
			t.Errorf("step = %q, want %q", got, "300")
		}
		if got := r.URL.Query().Get("end"); got == "" {
			t.Error("end should be set to current time when omitted")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up","start":"2026-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrometheusRange_TenantHeader(t *testing.T) {
	t.Parallel()

	t.Run("with tenant", func(t *testing.T) {
		t.Parallel()
		prom := newTestPrometheusRange(t, "my-org", func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("X-Scope-OrgID"); got != "my-org" {
				t.Errorf("X-Scope-OrgID = %q, want %q", got, "my-org")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		})
		_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up","start":"2026-01-01T00:00:00Z"}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("without tenant", func(t *testing.T) {
		t.Parallel()
		prom := newTestPrometheusRange(t, "", func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("X-Scope-OrgID"); got != "" {
				t.Errorf("X-Scope-OrgID = %q, want empty", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		})
		_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up","start":"2026-01-01T00:00:00Z"}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func FuzzPrometheusRangeExecute(f *testing.F) { //nolint:dupl // Similar fuzz test exists for Loki.Execute, but the input parameters and expected output are different enough that it's worth having a separate test.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer srv.Close()

	prom := NewPrometheusQueryRange(srv.URL, "test")

	f.Add(`{"query":"up","start":"2026-01-01T00:00:00Z"}`)
	f.Add(`{"query":"","start":"2026-01-01T00:00:00Z"}`)
	f.Add(`{"query":"up","start":""}`)
	f.Add(`{}`)
	f.Add(`not json`)
	f.Add(`{"query":"rate(http_requests_total[5m])","start":"2026-01-01T00:00:00Z","end":"2026-01-01T06:00:00Z","step":"1m"}`)
	f.Add(`{"query":"up","start":"2026-01-01T00:00:00Z","extra":true}`)
	f.Add(string([]byte{0x00, 0xff, 0xfe}))

	f.Fuzz(func(_ *testing.T, params string) {
		// Must not panic
		_, _ = prom.Execute(context.Background(), json.RawMessage(params))
	})
}
