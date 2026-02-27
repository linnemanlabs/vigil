package authmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

func TestBearerToken_ValidToken(t *testing.T) {
	t.Parallel()

	h := BearerToken("secret-token-123")(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Authorization", "Bearer secret-token-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestBearerToken_MissingHeader(t *testing.T) {
	t.Parallel()

	h := BearerToken("secret")(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestBearerToken_WrongPrefix(t *testing.T) {
	t.Parallel()

	h := BearerToken("secret")(okHandler)

	tests := []struct {
		name  string
		value string
	}{
		{"Basic auth", "Basic dXNlcjpwYXNz"},
		{"lowercase bearer", "bearer secret"},
		{"no prefix", "secret"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			if tt.value != "" {
				req.Header.Set("Authorization", tt.value)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestBearerToken_InvalidToken(t *testing.T) {
	t.Parallel()

	h := BearerToken("correct-token")(okHandler)

	tests := []struct {
		name  string
		token string
	}{
		{"wrong token", "wrong-token"},
		{"partial match", "correct"},
		{"token with suffix", "correct-token-extra"},
		{"empty token", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestBearerToken_PassesRequestThrough(t *testing.T) {
	t.Parallel()

	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	})

	h := BearerToken("tok")(inner)

	req := httptest.NewRequest(http.MethodPost, "/test", http.NoBody)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Error("inner handler was not called")
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
}
