package memstore

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/linnemanlabs/vigil/internal/triage"
)

func TestStore_PutAndGet(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	r := &triage.Result{ID: "t-1", Fingerprint: "fp-1", Status: triage.StatusPending}
	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.Get(ctx, "t-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected result to be found")
	}
	if got.ID != "t-1" {
		t.Errorf("ID = %q, want %q", got.ID, "t-1")
	}
	if got.Fingerprint != "fp-1" {
		t.Errorf("Fingerprint = %q, want %q", got.Fingerprint, "fp-1")
	}
}

func TestStore_GetMissing(t *testing.T) {
	t.Parallel()

	s := New()
	_, ok, err := s.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for missing ID")
	}
}

func TestStore_GetByFingerprint(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	r := &triage.Result{ID: "t-2", Fingerprint: "fp-abc", Status: triage.StatusPending}
	if err := s.Put(ctx, r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := s.GetByFingerprint(ctx, "fp-abc")
	if err != nil {
		t.Fatalf("GetByFingerprint: %v", err)
	}
	if !ok {
		t.Fatal("expected result to be found by fingerprint")
	}
	if got.ID != "t-2" {
		t.Errorf("ID = %q, want %q", got.ID, "t-2")
	}
}

func TestStore_GetByFingerprintMissing(t *testing.T) {
	t.Parallel()

	s := New()
	_, ok, err := s.GetByFingerprint(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetByFingerprint: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for missing fingerprint")
	}
}

func TestStore_PutOverwrites(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	_ = s.Put(ctx, &triage.Result{ID: "t-3", Fingerprint: "fp-3", Status: triage.StatusPending})
	_ = s.Put(ctx, &triage.Result{ID: "t-3", Fingerprint: "fp-3", Status: triage.StatusComplete, Analysis: "done"})

	got, ok, err := s.Get(ctx, "t-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected result to be found")
	}
	if got.Status != triage.StatusComplete {
		t.Errorf("Status = %q, want %q", got.Status, triage.StatusComplete)
	}
	if got.Analysis != "done" {
		t.Errorf("Analysis = %q, want %q", got.Analysis, "done")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	s := New()
	ctx := context.Background()
	const n = 100

	var wg sync.WaitGroup
	wg.Add(n * 2)

	for i := range n {
		id := fmt.Sprintf("id-%d", i)
		fp := fmt.Sprintf("fp-%d", i)

		go func() {
			defer wg.Done()
			_ = s.Put(ctx, &triage.Result{ID: id, Fingerprint: fp, Status: triage.StatusPending})
		}()

		go func() {
			defer wg.Done()
			_, _, _ = s.Get(ctx, id)
			_, _, _ = s.GetByFingerprint(ctx, fp)
		}()
	}

	wg.Wait()
}
