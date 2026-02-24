package alertapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/linnemanlabs/go-core/log"
)

func TestNew_NilLogger(t *testing.T) {
	t.Parallel()

	api := New(nil)
	if api == nil {
		t.Fatal("New(nil) returned nil API")
	}
	if api.logger == nil {
		t.Fatal("New(nil) left logger nil; expected Nop logger")
	}
}

func TestNew_WithLogger(t *testing.T) {
	t.Parallel()

	l := log.Nop()
	api := New(l)
	if api == nil {
		t.Fatal("New(logger) returned nil API")
	}
	if api.logger == nil {
		t.Fatal("New(logger) left logger nil")
	}
}

func TestRegisterRoutes_AlertIngestion(t *testing.T) {
	t.Parallel()

	api := New(nil)
	r := chi.NewRouter()
	api.RegisterRoutes(r)

	tests := []struct {
		name       string
		method     string
		wantStatus int
	}{
		{"POST accepted", http.MethodPost, http.StatusAccepted},
		{"GET not allowed", http.MethodGet, http.StatusMethodNotAllowed},
		{"PUT not allowed", http.MethodPut, http.StatusMethodNotAllowed},
		{"DELETE not allowed", http.MethodDelete, http.StatusMethodNotAllowed},
		{"PATCH not allowed", http.MethodPatch, http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tt.method, "/api/v1/alerts", http.NoBody)
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

	api := New(nil)
	r := chi.NewRouter()
	api.RegisterRoutes(r)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"GET with UUID", http.MethodGet, "/api/v1/triage/550e8400-e29b-41d4-a716-446655440000", http.StatusOK},
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

	api := New(nil)
	r := chi.NewRouter()
	api.RegisterRoutes(r)

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

func FuzzAlertIngestion(f *testing.F) {
	api := New(nil)
	r := chi.NewRouter()
	api.RegisterRoutes(r)

	// Seeds: empty, valid JSON, invalid JSON, binary garbage, various content types
	seeds := []struct {
		body        []byte
		contentType string
	}{
		{nil, ""},
		{[]byte(""), "application/json"},
		{[]byte("{}"), "application/json"},
		{[]byte(`{"alert":"test","severity":"high"}`), "application/json"},
		{[]byte(`[{"alert":"a"},{"alert":"b"}]`), "application/json"},
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

		// Must not panic. Currently always returns 202.
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("POST /api/v1/alerts with body len=%d content-type=%q = %d, want %d",
				len(body), contentType, rec.Code, http.StatusAccepted)
		}
	})
}
