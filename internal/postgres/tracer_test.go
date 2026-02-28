package postgres

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShortenFuncName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"full path", "github.com/linnemanlabs/vigil/internal/triage/pgstore.(*Store).Get", "(*Store).Get"},
		{"already short", "(*Store).Get", "Get"},
		{"empty string", "", ""},
		{"no dots", "main", "main"},
		{"no slashes", "pgstore.(*Store).Get", "(*Store).Get"},
		{"single segment", "foo.Bar", "Bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shortenFuncName(tt.in)
			if got != tt.want {
				t.Errorf("shortenFuncName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestReqDBStats_AddQuery(t *testing.T) {
	t.Parallel()

	s := &ReqDBStats{}

	s.AddQuery(10*time.Millisecond, nil)
	s.AddQuery(20*time.Millisecond, errors.New("timeout"))
	s.AddQuery(5*time.Millisecond, nil)

	if s.QueryCount != 3 {
		t.Errorf("QueryCount = %d, want 3", s.QueryCount)
	}
	if s.TotalDuration != 35*time.Millisecond {
		t.Errorf("TotalDuration = %v, want 35ms", s.TotalDuration)
	}
	if s.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", s.ErrorCount)
	}
}

func TestReqDBStatsContext_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := NewReqDBStatsContext(context.Background())
	got, ok := ReqDBStatsFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got == nil {
		t.Fatal("expected non-nil stats")
	}

	// Verify it's the same pointer
	got.AddQuery(time.Millisecond, nil)
	got2, _ := ReqDBStatsFromContext(ctx)
	if got2.QueryCount != 1 {
		t.Errorf("QueryCount = %d, want 1 (same pointer)", got2.QueryCount)
	}
}

func TestReqDBStatsFromContext_Missing(t *testing.T) {
	t.Parallel()

	_, ok := ReqDBStatsFromContext(context.Background())
	if ok {
		t.Error("expected ok=false for plain context")
	}
}

func TestWithHTTPMethod_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := WithHTTPMethod(context.Background(), "POST")
	got := httpMethodFromContext(ctx)
	if got != "POST" {
		t.Errorf("httpMethodFromContext = %q, want %q", got, "POST")
	}
}

func TestWithHTTPMethod_Empty(t *testing.T) {
	t.Parallel()

	ctx := WithHTTPMethod(context.Background(), "")
	got := httpMethodFromContext(ctx)
	if got != "" {
		t.Errorf("httpMethodFromContext = %q, want empty", got)
	}
}

func TestSetQueryObserver(t *testing.T) {
	t.Parallel()

	// Save and restore the global to avoid test pollution.
	defer SetQueryObserver(nil)

	called := false
	obs := QueryObserverFunc(func(_ context.Context, _, _, _ string, _ time.Duration) {
		called = true
	})

	SetQueryObserver(obs)
	got := getQueryObserver()
	if got == nil {
		t.Fatal("expected non-nil observer after Set")
	}
	got.ObserveQuery(context.Background(), "GET", "/test", "ok", time.Millisecond)
	if !called {
		t.Error("observer was not called")
	}

	SetQueryObserver(nil)
	got = getQueryObserver()
	if got != nil {
		t.Errorf("expected nil observer after Set(nil), got %v", got)
	}
}
