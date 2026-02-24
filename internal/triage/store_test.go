package triage

import (
	"fmt"
	"sync"
	"testing"
)

func TestStore_PutAndGet(t *testing.T) {
	t.Parallel()

	s := NewStore()
	r := &Result{ID: "t-1", Fingerprint: "fp-1", Status: StatusPending}
	s.Put(r)

	got, ok := s.Get("t-1")
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

	s := NewStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("expected ok=false for missing ID")
	}
}

func TestStore_GetByFingerprint(t *testing.T) {
	t.Parallel()

	s := NewStore()
	r := &Result{ID: "t-2", Fingerprint: "fp-abc", Status: StatusPending}
	s.Put(r)

	got, ok := s.GetByFingerprint("fp-abc")
	if !ok {
		t.Fatal("expected result to be found by fingerprint")
	}
	if got.ID != "t-2" {
		t.Errorf("ID = %q, want %q", got.ID, "t-2")
	}
}

func TestStore_GetByFingerprintMissing(t *testing.T) {
	t.Parallel()

	s := NewStore()
	_, ok := s.GetByFingerprint("nonexistent")
	if ok {
		t.Fatal("expected ok=false for missing fingerprint")
	}
}

func TestStore_PutOverwrites(t *testing.T) {
	t.Parallel()

	s := NewStore()
	s.Put(&Result{ID: "t-3", Fingerprint: "fp-3", Status: StatusPending})
	s.Put(&Result{ID: "t-3", Fingerprint: "fp-3", Status: StatusComplete, Analysis: "done"})

	got, ok := s.Get("t-3")
	if !ok {
		t.Fatal("expected result to be found")
	}
	if got.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", got.Status, StatusComplete)
	}
	if got.Analysis != "done" {
		t.Errorf("Analysis = %q, want %q", got.Analysis, "done")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	s := NewStore()
	const n = 100

	var wg sync.WaitGroup
	wg.Add(n * 2)

	for i := range n {
		id := fmt.Sprintf("id-%d", i)
		fp := fmt.Sprintf("fp-%d", i)

		go func() {
			defer wg.Done()
			s.Put(&Result{ID: id, Fingerprint: fp, Status: StatusPending})
		}()

		go func() {
			defer wg.Done()
			s.Get(id)
			s.GetByFingerprint(fp)
		}()
	}

	wg.Wait()
}
