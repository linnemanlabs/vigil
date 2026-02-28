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

func newTestLoki(t *testing.T, tenantID string, handler http.HandlerFunc) *LokiQuery {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewLokiQuery(srv.URL, tenantID)
}

func TestLokiQuery_Success(t *testing.T) {
	t.Parallel()

	loki := newTestLoki(t, "my-tenant", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Scope-OrgID"); got != "my-tenant" {
			t.Errorf("X-Scope-OrgID = %q, want %q", got, "my-tenant")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"job":"varlogs"},"values":[["1234","line1"],["1235","line2"]]}
		]}}`)
	})

	out, err := loki.Execute(context.Background(), json.RawMessage(`{"query":"{job=\"varlogs\"}"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if parsed["stream_count"] != float64(1) {
		t.Errorf("stream_count = %v, want 1", parsed["stream_count"])
	}
	if parsed["line_count"] != float64(2) {
		t.Errorf("line_count = %v, want 2", parsed["line_count"])
	}
	if parsed["truncated"] != false {
		t.Errorf("truncated = %v, want false", parsed["truncated"])
	}
	lines, ok := parsed["lines"].([]any)
	if !ok || len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %v", parsed["lines"])
	}
}

func TestLokiQuery_EmptyQuery(t *testing.T) {
	t.Parallel()

	loki := newTestLoki(t, "test", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not have made HTTP request")
	})

	_, err := loki.Execute(context.Background(), json.RawMessage(`{"query":""}`))
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q, want it to mention 'required'", err.Error())
	}
}

func TestLokiQuery_InvalidParams(t *testing.T) {
	t.Parallel()

	loki := newTestLoki(t, "test", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not have made HTTP request")
	})

	_, err := loki.Execute(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid params")
	}
	if !strings.Contains(err.Error(), "invalid params") {
		t.Errorf("error = %q, want it to mention 'invalid params'", err.Error())
	}
}

func TestLokiQuery_HTTPError(t *testing.T) {
	t.Parallel()

	loki := newTestLoki(t, "test", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "internal error")
	})

	_, err := loki.Execute(context.Background(), json.RawMessage(`{"query":"{job=\"a\"}"}`))
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status code", err.Error())
	}
}

func TestLokiQuery_NonSuccessStatus(t *testing.T) {
	t.Parallel()

	loki := newTestLoki(t, "test", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"error","data":{"resultType":"streams","result":[]}}`)
	})

	_, err := loki.Execute(context.Background(), json.RawMessage(`{"query":"{job=\"a\"}"}`))
	if err == nil {
		t.Fatal("expected error for non-success loki status")
	}
	if !strings.Contains(err.Error(), "loki query failed") {
		t.Errorf("error = %q, want it to mention 'loki query failed'", err.Error())
	}
}

func TestLokiQuery_UnparsableResponse(t *testing.T) {
	t.Parallel()

	loki := newTestLoki(t, "test", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "this is not json at all")
	})

	out, err := loki.Execute(context.Background(), json.RawMessage(`{"query":"{job=\"a\"}"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v (unparsable body should return raw)", err)
	}
	if !strings.Contains(string(out), "this is not json at all") {
		t.Errorf("output = %q, want raw body", string(out))
	}
}

func TestLokiQuery_NoTenantHeader(t *testing.T) {
	t.Parallel()

	loki := newTestLoki(t, "", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Scope-OrgID"); got != "" {
			t.Errorf("X-Scope-OrgID = %q, want empty (no tenant)", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	})

	_, err := loki.Execute(context.Background(), json.RawMessage(`{"query":"{job=\"a\"}"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLokiQuery_LimitClamping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantLimit string
	}{
		{"zero defaults to 100", `{"query":"{job=\"a\"}","limit":0}`, "100"},
		{"negative defaults to 100", `{"query":"{job=\"a\"}","limit":-5}`, "100"},
		{"over max caps to 500", `{"query":"{job=\"a\"}","limit":9999}`, "500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			loki := newTestLoki(t, "test", func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("limit"); got != tt.wantLimit {
					t.Errorf("limit = %q, want %q", got, tt.wantLimit)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
			})

			_, err := loki.Execute(context.Background(), json.RawMessage(tt.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLokiQuery_Truncation(t *testing.T) {
	t.Parallel()

	// Build a response with >100 lines across streams
	values := make([]string, 0, 120)
	for i := range 120 {
		values = append(values, fmt.Sprintf(`["%d","line-%d"]`, 1000+i, i))
	}

	loki := newTestLoki(t, "test", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"job":"a"},"values":[%s]}
		]}}`, strings.Join(values, ","))
	})

	out, err := loki.Execute(context.Background(), json.RawMessage(`{"query":"{job=\"a\"}"}`))
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
	lines, ok := parsed["lines"].([]any)
	if !ok {
		t.Fatalf("lines is not an array: %T", parsed["lines"])
	}
	if len(lines) != 100 {
		t.Errorf("len(lines) = %d, want 100", len(lines))
	}
}

func TestFlattenStreams(t *testing.T) {
	t.Parallel()

	streams := []lokiStream{
		{
			Stream: map[string]string{"job": "a"},
			Values: [][]string{
				{"1000", "line-a1"},
				{"1001"}, // short entry, should be skipped
				{"1002", "line-a2"},
			},
		},
		{
			Stream: map[string]string{"job": "b"},
			Values: [][]string{
				{"2000", "line-b1"},
				{"2001", "line-b2"},
			},
		},
	}

	lines := flattenStreams(streams, 3)

	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3 (limit enforced across streams)", len(lines))
	}

	// First entry in stream "a" should have labels
	if lines[0].Labels == nil || lines[0].Labels["job"] != "a" {
		t.Errorf("lines[0].Labels = %v, want {job:a}", lines[0].Labels)
	}
	if lines[0].Line != "line-a1" {
		t.Errorf("lines[0].Line = %q, want %q", lines[0].Line, "line-a1")
	}

	// Second entry in stream "a" should NOT have labels (includeLabels=false after first)
	if lines[1].Labels != nil {
		t.Errorf("lines[1].Labels = %v, want nil", lines[1].Labels)
	}
	if lines[1].Line != "line-a2" {
		t.Errorf("lines[1].Line = %q, want %q", lines[1].Line, "line-a2")
	}

	// Third entry is from stream "b" and should have labels (first entry of that stream)
	if lines[2].Labels == nil || lines[2].Labels["job"] != "b" {
		t.Errorf("lines[2].Labels = %v, want {job:b}", lines[2].Labels)
	}
	if lines[2].Line != "line-b1" {
		t.Errorf("lines[2].Line = %q, want %q", lines[2].Line, "line-b1")
	}
}

func FuzzLokiExecute(f *testing.F) { //nolint:dupl // Similar fuzz test exists for PrometheusQuery.Execute, but the input parameters and expected output are different enough that it's worth having a separate test.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	}))
	defer srv.Close()

	loki := NewLokiQuery(srv.URL, "test")

	f.Add(`{"query":"{job=\"varlogs\"}"}`)
	f.Add(`{"query":""}`)
	f.Add(`{}`)
	f.Add(`not json`)
	f.Add(`{"query":"{node=\"host\"} |= \"error\"","start":"2026-01-01T00:00:00Z","end":"2026-01-01T01:00:00Z","limit":50}`)
	f.Add(`{"query":"{job=\"a\"}","limit":-1}`)
	f.Add(`{"query":"{job=\"a\"}","limit":99999}`)
	f.Add(string([]byte{0x00, 0xff, 0xfe}))

	f.Fuzz(func(_ *testing.T, params string) {
		// Must not panic
		_, _ = loki.Execute(context.Background(), json.RawMessage(params))
	})
}
