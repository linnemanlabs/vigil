package alertapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/vigil/internal/alert"
	"github.com/linnemanlabs/vigil/internal/triage"
)

// stubTriageService implements TriageService for testing.
type stubTriageService struct {
	submitFn func(ctx context.Context, al *alert.Alert) (*triage.SubmitResult, error)
	getFn    func(ctx context.Context, id string) (*triage.Result, bool, error)
}

func (s *stubTriageService) Submit(ctx context.Context, al *alert.Alert) (*triage.SubmitResult, error) {
	if s.submitFn != nil {
		return s.submitFn(ctx, al)
	}
	return &triage.SubmitResult{ID: "stub-id"}, nil
}

func (s *stubTriageService) Get(ctx context.Context, id string) (*triage.Result, bool, error) {
	if s.getFn != nil {
		return s.getFn(ctx, id)
	}
	return nil, false, nil
}

func newTestAPI(t *testing.T) (*API, *stubTriageService) {
	t.Helper()
	svc := &stubTriageService{}
	api := New(nil, svc)
	return api, svc
}

func newTestRouter(t *testing.T) (chi.Router, *stubTriageService) {
	t.Helper()
	api, svc := newTestAPI(t)
	r := chi.NewRouter()
	api.RegisterRoutes(r)
	return r, svc
}

//  New / constructor

func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	svc := &stubTriageService{}
	api := New(nil, svc)
	if api == nil {
		t.Fatal("New(nil, svc) returned nil API")
	}
	if api.logger == nil {
		t.Fatal("New(nil, svc) left logger nil; expected Nop logger")
	}
}

func TestNew_WithLogger(t *testing.T) {
	t.Parallel()

	l := log.Nop()
	svc := &stubTriageService{}
	api := New(l, svc)
	if api == nil {
		t.Fatal("New(logger, svc) returned nil API")
	}
	if api.logger == nil {
		t.Fatal("New(logger, svc) left logger nil")
	}
}

func TestNew_NilService_Panics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("New(nil, nil) did not panic; expected panic for nil service")
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
		{"GET with ULID-like ID", http.MethodGet, "/api/v1/triage/01H5K3ABCDEFGHJKMNPQRS", http.StatusNotFound},
		{"GET with integer", http.MethodGet, "/api/v1/triage/42", http.StatusNotFound},
		{"GET with short string", http.MethodGet, "/api/v1/triage/abc", http.StatusNotFound},
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

	r, svc := newTestRouter(t)
	svc.submitFn = func(_ context.Context, al *alert.Alert) (*triage.SubmitResult, error) {
		if al.Labels["alertname"] != "HighCPU" {
			t.Errorf("alertname = %q, want HighCPU", al.Labels["alertname"])
		}
		return &triage.SubmitResult{ID: "test-id-001"}, nil
	}

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
	if accepted[0].(string) != "test-id-001" {
		t.Errorf("accepted ID = %q, want %q", accepted[0], "test-id-001")
	}
}

func TestHandleIngestAlert_SkipsResolvedAlerts(t *testing.T) { //nolint:dupl // similar to TestHandleIngestAlert_DedupPendingFingerprint but with resolved status and expecting skip
	t.Parallel()

	r, svc := newTestRouter(t)
	svc.submitFn = func(_ context.Context, _ *alert.Alert) (*triage.SubmitResult, error) {
		return &triage.SubmitResult{Skipped: true, Reason: "not firing"}, nil
	}

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

	if accepted, ok := resp["accepted"].([]any); ok && len(accepted) != 0 {
		t.Errorf("expected 0 accepted IDs for resolved alert, got %d", len(accepted))
	}
}

func TestHandleIngestAlert_DedupPendingFingerprint(t *testing.T) { //nolint:dupl // similar to TestHandleIngestAlert_ValidFiringAlert but with duplicate fingerprint and expecting skip
	t.Parallel()

	r, svc := newTestRouter(t)
	svc.submitFn = func(_ context.Context, _ *alert.Alert) (*triage.SubmitResult, error) {
		return &triage.SubmitResult{Skipped: true, Reason: "duplicate"}, nil
	}

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

func TestHandleIngestAlert_MultipleAlerts(t *testing.T) {
	t.Parallel()

	r, svc := newTestRouter(t)
	callCount := 0
	svc.submitFn = func(_ context.Context, al *alert.Alert) (*triage.SubmitResult, error) {
		callCount++
		// second alert is resolved, service would skip it
		if al.Labels["alertname"] == "B" {
			return &triage.SubmitResult{Skipped: true, Reason: "not firing"}, nil
		}
		return &triage.SubmitResult{ID: al.Labels["alertname"] + "-id"}, nil
	}

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

// Triage GET handler

func TestHandleGetTriage_Found(t *testing.T) {
	t.Parallel()

	r, svc := newTestRouter(t)
	svc.getFn = func(_ context.Context, id string) (*triage.Result, bool, error) {
		if id == "test-123" {
			return &triage.Result{
				ID:       "test-123",
				Status:   triage.StatusComplete,
				Analysis: "all good",
			}, true, nil
		}
		return nil, false, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/triage/test-123", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var result triage.Result
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result.ID != "test-123" {
		t.Errorf("ID = %q, want %q", result.ID, "test-123")
	}
	if result.Analysis != "all good" {
		t.Errorf("analysis = %q, want %q", result.Analysis, "all good")
	}
}

func TestHandleGetTriage_NotFound(t *testing.T) {
	t.Parallel()

	r, _ := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/triage/nonexistent", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleGetTriage_StoreError(t *testing.T) {
	t.Parallel()

	r, svc := newTestRouter(t)
	svc.getFn = func(_ context.Context, _ string) (*triage.Result, bool, error) {
		return nil, false, errors.New("database connection lost")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/triage/some-id", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), "internal error") {
		t.Errorf("body = %q, want it to contain 'internal error'", rec.Body.String())
	}
}

func TestHandleIngestAlert_PartialSubmitError(t *testing.T) {
	t.Parallel()

	r, svc := newTestRouter(t)
	callIdx := 0
	svc.submitFn = func(_ context.Context, _ *alert.Alert) (*triage.SubmitResult, error) {
		callIdx++
		if callIdx == 1 {
			return nil, errors.New("db write failed")
		}
		return &triage.SubmitResult{ID: "ok-id"}, nil
	}

	body := `{
		"alerts": [
			{"status": "firing", "fingerprint": "fp-1", "labels": {"alertname": "A"}, "annotations": {}},
			{"status": "firing", "fingerprint": "fp-2", "labels": {"alertname": "B"}, "annotations": {}}
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
	if !ok || len(accepted) != 1 {
		t.Fatalf("expected 1 accepted ID (first failed, second succeeded), got %v", resp["accepted"])
	}
	if accepted[0].(string) != "ok-id" {
		t.Errorf("accepted ID = %q, want %q", accepted[0], "ok-id")
	}
}

// Fuzz

func FuzzAlertIngestion(f *testing.F) {
	svc := &stubTriageService{}
	api := New(nil, svc)
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
