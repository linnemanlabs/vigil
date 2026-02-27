package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/vigil/internal/triage"
)

func TestSend_PostsToWebhook(t *testing.T) {
	t.Parallel()

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(srv.URL, log.Nop())
	result := &triage.Result{
		ID:          "01JN123",
		Status:      triage.StatusComplete,
		Alert:       "HighMemoryUsage",
		Severity:    "critical",
		Analysis:    "Memory is high.",
		Duration:    23.4,
		TokensIn:    800,
		TokensOut:   450,
		ToolCalls:   3,
		Model:       "claude-sonnet-4-20250514",
		CompletedAt: time.Date(2026, 2, 26, 14, 23, 0, 0, time.UTC),
	}

	if err := n.Send(context.Background(), result); err != nil {
		t.Fatalf("Send: %v", err)
	}

	blocks, ok := got["blocks"].([]any)
	if !ok {
		t.Fatal("expected blocks array in payload")
	}

	// header, divider, fields, divider, analysis, divider, context = 7 blocks
	if len(blocks) != 7 {
		t.Errorf("blocks count = %d, want 7", len(blocks))
	}

	// Verify header contains alert name and critical emoji
	header := blocks[0].(map[string]any)
	headerText := header["text"].(map[string]any)["text"].(string)
	if !strings.Contains(headerText, "HighMemoryUsage") {
		t.Errorf("header text = %q, want to contain HighMemoryUsage", headerText)
	}
	if !strings.Contains(headerText, "\U0001f534") {
		t.Errorf("header should contain red circle for critical severity")
	}
}

func TestSend_NoOpWithoutURL(t *testing.T) {
	t.Parallel()

	n := New("", log.Nop())
	if err := n.Send(context.Background(), &triage.Result{}); err != nil {
		t.Fatalf("Send with empty URL should be no-op, got: %v", err)
	}
}

func TestSend_TruncatesLongAnalysis(t *testing.T) {
	t.Parallel()

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	longAnalysis := strings.Repeat("x", 4000)
	n := New(srv.URL, log.Nop())
	err := n.Send(context.Background(), &triage.Result{
		ID:       "01JN456",
		Status:   triage.StatusComplete,
		Analysis: longAnalysis,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	blocks := got["blocks"].([]any)
	analysisSection := blocks[4].(map[string]any)
	text := analysisSection["text"].(map[string]any)["text"].(string)

	// Text includes the "*Analysis*\n\n" prefix, so the analysis portion is what follows.
	// The analysis itself should be truncated to maxAnalysisLen (3000) chars.
	if len(text) > maxAnalysisLen+len("*Analysis*\n\n") {
		t.Errorf("analysis text length = %d, expected <= %d", len(text), maxAnalysisLen+len("*Analysis*\n\n"))
	}
	if !strings.HasSuffix(text, "...") {
		t.Error("expected truncated analysis to end with ...")
	}
}

func TestSeverityEmoji(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   triage.Status
		severity string
		want     string
	}{
		{"failed", triage.StatusFailed, "warning", "\U0001f534"},
		{"critical", triage.StatusComplete, "critical", "\U0001f534"},
		{"warning", triage.StatusComplete, "warning", "\U0001f7e1"},
		{"info", triage.StatusComplete, "info", "\U0001f7e2"},
		{"empty", triage.StatusComplete, "", "\U0001f7e2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := severityEmoji(tt.status, tt.severity)
			if got != tt.want {
				t.Errorf("severityEmoji(%q, %q) = %q, want %q", tt.status, tt.severity, got, tt.want)
			}
		})
	}
}

func TestShortModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"claude-sonnet-4-20250514", "claude-sonnet-4"},
		{"claude-opus-4-20250514", "claude-opus-4"},
		{"gpt-4o", "gpt-4o"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			if got := shortModel(tt.input); got != tt.want {
				t.Errorf("shortModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func FuzzSlackBuild(f *testing.F) {
	f.Add("HighCPU", "critical", "CPU is very high on node-1.", "claude-sonnet-4-20250514")
	f.Add("", "", "", "")
	f.Add("<@U123> mention", "warning", "*bold* _italic_ ~strike~", "model")
	f.Add("alert\x00\x01\x02", "sev\nline", "analysis\ttab", "m\x00del")
	f.Add(strings.Repeat("A", 5000), "critical", strings.Repeat("x", 10000), "model-name-20260101")
	f.Add("test", "info", "```code block``` and <http://example.com|link>", "gpt-4o")

	f.Fuzz(func(t *testing.T, alert, severity, analysis, model string) {
		result := &triage.Result{
			ID:          "fuzz-id",
			Status:      triage.StatusComplete,
			Alert:       alert,
			Severity:    severity,
			Analysis:    analysis,
			Model:       model,
			Duration:    1.0,
			TokensIn:    100,
			TokensOut:   50,
			ToolCalls:   1,
			CompletedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}

		// Must not panic
		msg := buildMessage(result)

		// Must produce valid JSON
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("buildMessage produced non-marshalable output: %v", err)
		}

		// Must round-trip
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("buildMessage JSON does not round-trip: %v", err)
		}

		blocks, ok := decoded["blocks"].([]any)
		if !ok {
			t.Fatal("expected blocks array")
		}
		if len(blocks) != 7 {
			t.Fatalf("blocks count = %d, want 7", len(blocks))
		}
	})
}

func TestSend_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	n := New(srv.URL, log.Nop())
	err := n.Send(context.Background(), &triage.Result{
		ID:     "01JN789",
		Status: triage.StatusComplete,
	})
	if err == nil {
		t.Fatal("expected error on non-OK status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want to contain status code 500", err.Error())
	}
}
