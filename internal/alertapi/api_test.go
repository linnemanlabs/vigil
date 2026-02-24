package alertapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/vigil/internal/triage"
)

func newTestAPI(t *testing.T) (*API, *triage.Store) {
	t.Helper()
	store := triage.NewStore()
	api := New(nil, store)
	return api, store
}

func newTestRouter(t *testing.T) (chi.Router, *triage.Store) {
	t.Helper()
	api, store := newTestAPI(t)
	r := chi.NewRouter()
	api.RegisterRoutes(r)
	return r, store
}

//  New / constructor

func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	store := triage.NewStore()
	api := New(nil, store)
	if api == nil {
		t.Fatal("New(nil, store) returned nil API")
	}
	if api.logger == nil {
		t.Fatal("New(nil, store) left logger nil; expected Nop logger")
	}
}

func TestNew_WithLogger(t *testing.T) {
	t.Parallel()

	l := log.Nop()
	store := triage.NewStore()
	api := New(l, store)
	if api == nil {
		t.Fatal("New(logger, store) returned nil API")
	}
	if api.logger == nil {
		t.Fatal("New(logger, store) left logger nil")
	}
}

func TestNew_NilStore_Panics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("New(nil, nil) did not panic; expected panic for nil store")
		}
	}()
	New(nil, nil)
}

// Routing

func TestRegisterRoutes_AlertIngestion(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t)

	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
	}{
		{"POST valid webhook", http.MethodPost, `{"alerts":[{"status":"firing","fingerprint":"abc123","labels":{"alertname":"TestAlert","severity":"critical"},"annotations":{"summary":"test"}}]}`, http.StatusAccepted},
		{"POST invalid JSON", http.MethodPost, `{bad`, http.StatusBadRequest},
		{"GET not allowed", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"PUT not allowed", http.MethodPut, "", http.StatusMethodNotAllowed},
		{"DELETE not allowed", http.MethodDelete, "", http.StatusMethodNotAllowed},
		{"PATCH not allowed", http.MethodPatch, "", http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body *strings.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tt.method, "/api/v1/alerts", body)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("%s /api/v1/alerts = %d, want %d", tt.method, rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestRegisterRoutes_Triage(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"GET with ULID-like ID", http.MethodGet, "/api/v1/triage/01H5K3ABCDEFGHJKMNPQRS", http.StatusOK},
		{"GET with integer", http.MethodGet, "/api/v1/triage/42", http.StatusOK},
		{"GET with short string", http.MethodGet, "/api/v1/triage/abc", http.StatusOK},
		{"POST not allowed", http.MethodPost, "/api/v1/triage/123", http.StatusMethodNotAllowed},
		{"PUT not allowed", http.MethodPut, "/api/v1/triage/123", http.StatusMethodNotAllowed},
		{"DELETE not allowed", http.MethodDelete, "/api/v1/triage/123", http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tt.method, tt.path, http.NoBody)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestRegisterRoutes_NotFound(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t)

	paths := []string{
		"/",
		"/api/v1",
		"/api/v2/alerts",
		"/api/v1/triage",
		"/api/v1/triage/",
		"/api/v1/unknown",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want %d", path, rec.Code, http.StatusNotFound)
			}
		})
	}
}

// Alert ingestion logic

func TestHandleIngestAlert_ValidFiringAlert(t *testing.T) {
	t.Parallel()

	r, store := newTestRouter(t)

	body := `{
		"alerts": [{
			"status": "firing",
			"fingerprint": "fp-001",
			"labels": {"alertname": "HighCPU", "severity": "critical"},
			"annotations": {"summary": "CPU is too high"}
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	accepted, ok := resp["accepted"].([]any)
	if !ok || len(accepted) != 1 {
		t.Fatalf("expected 1 accepted ID, got %v", resp["accepted"])
	}

	id := accepted[0].(string)

	// Give the async goroutine a moment to complete
	time.Sleep(50 * time.Millisecond)

	result, ok := store.Get(id)
	if !ok {
		t.Fatalf("triage result %q not found in store", id)
	}
	if result.Alert != "HighCPU" {
		t.Errorf("alert name = %q, want %q", result.Alert, "HighCPU")
	}
	if result.Severity != "critical" {
		t.Errorf("severity = %q, want %q", result.Severity, "critical")
	}
	if result.Summary != "CPU is too high" {
		t.Errorf("summary = %q, want %q", result.Summary, "CPU is too high")
	}
	if result.Fingerprint != "fp-001" {
		t.Errorf("fingerprint = %q, want %q", result.Fingerprint, "fp-001")
	}
}

func TestHandleIngestAlert_SkipsResolvedAlerts(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t)

	body := `{
		"alerts": [{
			"status": "resolved",
			"fingerprint": "fp-resolved",
			"labels": {"alertname": "Resolved"},
			"annotations": {}
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// accepted is either null or an empty array - both are fine
	if accepted, ok := resp["accepted"].([]any); ok && len(accepted) != 0 {
		t.Errorf("expected 0 accepted IDs for resolved alert, got %d", len(accepted))
	}
}

func TestHandleIngestAlert_DedupPendingFingerprint(t *testing.T) {
	t.Parallel()

	r, store := newTestRouter(t)

	// Pre-seed a pending result with the same fingerprint
	store.Put(&triage.Result{
		ID:          "existing-id",
		Fingerprint: "fp-dedup",
		Status:      triage.StatusPending,
	})

	body := `{
		"alerts": [{
			"status": "firing",
			"fingerprint": "fp-dedup",
			"labels": {"alertname": "Dup"},
			"annotations": {}
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if accepted, ok := resp["accepted"].([]any); ok && len(accepted) != 0 {
		t.Errorf("expected 0 accepted IDs for duplicate fingerprint, got %d", len(accepted))
	}
}

func TestHandleIngestAlert_DedupInProgressFingerprint(t *testing.T) {
	t.Parallel()

	r, store := newTestRouter(t)

	store.Put(&triage.Result{
		ID:          "existing-id",
		Fingerprint: "fp-inprog",
		Status:      triage.StatusInProgress,
	})

	body := `{
		"alerts": [{
			"status": "firing",
			"fingerprint": "fp-inprog",
			"labels": {"alertname": "InProg"},
			"annotations": {}
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if accepted, ok := resp["accepted"].([]any); ok && len(accepted) != 0 {
		t.Errorf("expected dedup to skip in_progress fingerprint, got %d accepted", len(accepted))
	}
}

func TestHandleIngestAlert_AllowsRetriageCompletedFingerprint(t *testing.T) {
	t.Parallel()

	r, store := newTestRouter(t)

	store.Put(&triage.Result{
		ID:          "old-id",
		Fingerprint: "fp-complete",
		Status:      triage.StatusComplete,
	})

	body := `{
		"alerts": [{
			"status": "firing",
			"fingerprint": "fp-complete",
			"labels": {"alertname": "Retriage"},
			"annotations": {}
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	accepted, ok := resp["accepted"].([]any)
	if !ok || len(accepted) != 1 {
		t.Fatalf("expected 1 accepted for completed-fingerprint retriage, got %v", resp["accepted"])
	}
}

func TestHandleIngestAlert_MultipleAlerts(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t)

	body := `{
		"alerts": [
			{"status": "firing", "fingerprint": "fp-a", "labels": {"alertname": "A"}, "annotations": {}},
			{"status": "resolved", "fingerprint": "fp-b", "labels": {"alertname": "B"}, "annotations": {}},
			{"status": "firing", "fingerprint": "fp-c", "labels": {"alertname": "C"}, "annotations": {}}
		]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	accepted, ok := resp["accepted"].([]any)
	if !ok || len(accepted) != 2 {
		t.Fatalf("expected 2 accepted (2 firing, 1 resolved skipped), got %v", resp["accepted"])
	}
}

func TestHandleIngestAlert_InvalidJSON(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleIngestAlert_EmptyAlerts(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(`{"alerts":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
}

// Triage async behavior

func TestTriage_SetsStatusToComplete(t *testing.T) {
	t.Parallel()

	r, store := newTestRouter(t)

	body := `{
		"alerts": [{
			"status": "firing",
			"fingerprint": "fp-triage",
			"labels": {"alertname": "TriageTest"},
			"annotations": {"summary": "testing triage"}
		}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	accepted := resp["accepted"].([]any)
	id := accepted[0].(string)

	// Wait for async triage to complete
	time.Sleep(100 * time.Millisecond)

	result, ok := store.Get(id)
	if !ok {
		t.Fatalf("triage result %q not found in store", id)
	}
	if result.Status != triage.StatusComplete {
		t.Errorf("status = %q, want %q", result.Status, triage.StatusComplete)
	}
	if result.Analysis == "" {
		t.Error("expected non-empty analysis after triage")
	}
}

// Fuzz

func FuzzAlertIngestion(f *testing.F) {
	store := triage.NewStore()
	api := New(nil, store)
	r := chi.NewRouter()
	api.RegisterRoutes(r)

	seeds := []struct {
		body        []byte
		contentType string
	}{
		{nil, ""},
		{[]byte(""), "application/json"},
		{[]byte("{}"), "application/json"},
		{[]byte(`{"alerts":[{"status":"firing","fingerprint":"f1","labels":{"alertname":"A"},"annotations":{}}]}`), "application/json"},
		{[]byte(`{"alerts":[{"status":"firing","fingerprint":"f1"},{"status":"resolved","fingerprint":"f2"}]}`), "application/json"},
		{[]byte("{invalid json"), "application/json"},
		{[]byte("\x00\x01\x02\xff\xfe"), "application/octet-stream"},
		{[]byte("<xml>not json</xml>"), "text/xml"},
		{[]byte(strings.Repeat("a", 10000)), "text/plain"},
	}
	for _, s := range seeds {
		f.Add(s.body, s.contentType)
	}

	f.Fuzz(func(t *testing.T, body []byte, contentType string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(string(body)))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()

		// Must not panic
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted && rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/v1/alerts with body len=%d content-type=%q = %d, want 202 or 400",
				len(body), contentType, rec.Code)
		}
	})
}
