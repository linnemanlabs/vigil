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

func newTestPrometheus(t *testing.T, handler http.HandlerFunc) *PrometheusQuery {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewPrometheusQuery(srv.URL, "test")
}

func TestPrometheusQuery_Success(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheus(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("query") != "up" {
			t.Errorf("query = %q, want %q", r.URL.Query().Get("query"), "up")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up"},"value":[1234,"1"]}]}}`)
	})

	out, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if parsed["result_type"] != "vector" {
		t.Errorf("result_type = %v, want %q", parsed["result_type"], "vector")
	}
	if parsed["truncated"] != false {
		t.Errorf("truncated = %v, want false", parsed["truncated"])
	}
}

func TestPrometheusQuery_WithTime(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheus(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("time"); got != "2024-01-01T00:00:00Z" {
			t.Errorf("time = %q, want %q", got, "2024-01-01T00:00:00Z")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up","time":"2024-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrometheusQuery_EmptyQuery(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheus(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not have made HTTP request")
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":""}`))
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q, want it to mention 'required'", err.Error())
	}
}

func TestPrometheusQuery_InvalidParams(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheus(t, func(_ http.ResponseWriter, _ *http.Request) {
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

func TestPrometheusQuery_HTTPError(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheus(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "internal error")
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up"}`))
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status code", err.Error())
	}
}

func TestPrometheusQuery_NonSuccessStatus(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheus(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"error","errorType":"bad_data","error":"parse error"}`)
	})

	_, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"bad{}"}`))
	if err == nil {
		t.Fatal("expected error for non-success prometheus status")
	}
	if !strings.Contains(err.Error(), "prometheus query failed") {
		t.Errorf("error = %q, want it to mention 'prometheus query failed'", err.Error())
	}
}

func TestPrometheusQuery_UnparsableResponse(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheus(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "this is not json at all")
	})

	out, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v (unparsable body should return raw)", err)
	}
	if !strings.Contains(string(out), "this is not json at all") {
		t.Errorf("output = %q, want raw body", string(out))
	}
}

func TestPrometheusQuery_Truncation(t *testing.T) {
	t.Parallel()

	prom := newTestPrometheus(t, func(w http.ResponseWriter, _ *http.Request) {
		var results = make([]string, 0, 60)
		for i := range 60 {
			results = append(results, fmt.Sprintf(`{"metric":{"i":"%d"},"value":[1234,"%d"]}`, i, i))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[%s]}}`, strings.Join(results, ","))
	})

	out, err := prom.Execute(context.Background(), json.RawMessage(`{"query":"up"}`))
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
	// result_count reflects original count
	if int(parsed["result_count"].(float64)) != 60 {
		t.Errorf("result_count = %v, want 60", parsed["result_count"])
	}
	// results array should be capped at 50
	results, ok := parsed["results"].([]any)
	if !ok {
		t.Fatalf("results is not an array: %T", parsed["results"])
	}
	if len(results) != 50 {
		t.Errorf("len(results) = %d, want 50", len(results))
	}
}

func FuzzPrometheusExecute(f *testing.F) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	defer srv.Close()

	prom := NewPrometheusQuery(srv.URL, "test")

	f.Add(`{"query":"up"}`)
	f.Add(`{"query":""}`)
	f.Add(`{}`)
	f.Add(`not json`)
	f.Add(`{"query":"rate(http_requests_total[5m])","time":"2024-01-01T00:00:00Z"}`)
	f.Add(`{"query":"up","extra_field":123}`)

	f.Fuzz(func(_ *testing.T, params string) {
		// Must not panic
		_, _ = prom.Execute(context.Background(), json.RawMessage(params))
	})
}
