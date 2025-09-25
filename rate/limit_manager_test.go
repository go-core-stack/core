package rate

import (
	"testing"

	"golang.org/x/time/rate"

	coreerrors "github.com/go-core-stack/core/errors"
)

func TestLimitManagerNewLimiter(t *testing.T) {
	mgr := NewLimitManager(100)

	lim, err := mgr.NewLimiter("worker", 10, 5)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}
	if lim.mgr != mgr {
		t.Fatalf("limiter manager mismatch: got %p want %p", lim.mgr, mgr)
	}
	if lim.key != "worker" {
		t.Fatalf("limiter key mismatch: got %q want %q", lim.key, "worker")
	}
	if lim.rate != 10 {
		t.Fatalf("limiter rate mismatch: got %d want %d", lim.rate, 10)
	}
	if lim.burst != 5 {
		t.Fatalf("limiter burst mismatch: got %d want %d", lim.burst, 5)
	}
	if lim.limiter.Limit() != rate.Limit(lim.rate) {
		t.Fatalf("initial limiter limit incorrect: got %v want %v", lim.limiter.Limit(), rate.Limit(lim.rate))
	}

	_, err = mgr.NewLimiter("worker", 10, 5)
	if err == nil {
		t.Fatalf("expected duplicate limiter creation to fail")
	}
	if !coreerrors.IsAlreadyExists(err) {
		t.Fatalf("expected AlreadyExists error, got %v", err)
	}
}

// TestLimitManagerUpdateInUseRedistributes ensures headroom is shared evenly
// and that limits reset when a limiter leaves the active set.
func TestLimitManagerUpdateInUseRedistributes(t *testing.T) {
	mgr := NewLimitManager(100)

	l1, err := mgr.NewLimiter("alpha", 30, 10)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}
	l2, err := mgr.NewLimiter("beta", 40, 10)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}

	l1.SetInUse(true)
	l2.SetInUse(true)

	if got := len(mgr.inUse); got != 2 {
		t.Fatalf("expected 2 active limiters, got %d", got)
	}
	if got := l1.limiter.Limit(); got < rate.Limit(30) {
		t.Fatalf("unexpected limit for alpha: got %v want more than %v", got, rate.Limit(30))
	}
	if got := l2.limiter.Limit(); got < rate.Limit(40) {
		t.Fatalf("unexpected limit for beta: got %v want more than %v", got, rate.Limit(40))
	}

	l1.SetInUse(false)

	if got := len(mgr.inUse); got != 1 {
		t.Fatalf("expected 1 active limiter after release, got %d", got)
	}
	if got := l1.limiter.Limit(); got != rate.Limit(l1.rate) {
		t.Fatalf("released limiter should reset to base rate: got %v want %v", got, rate.Limit(l1.rate))
	}
	if got := l2.limiter.Limit(); got != rate.Limit(100) {
		t.Fatalf("remaining limiter should consume full capacity: got %v want %v", got, rate.Limit(100))
	}
}

// TestLimitManagerSingleLimiterRelease verifies a single active limiter can
// claim the full capacity and returns to its base rate after release.
func TestLimitManagerSingleLimiterRelease(t *testing.T) {
	mgr := NewLimitManager(100)

	l, err := mgr.NewLimiter("solo", 30, 10)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetInUse should not panic on release: %v", r)
		}
	}()

	l.SetInUse(true)
	if got := l.limiter.Limit(); got != rate.Limit(100) {
		t.Fatalf("expected limiter to receive full capacity when active: got %v want %v", got, rate.Limit(100))
	}

	l.SetInUse(false)
	if len(mgr.inUse) != 0 {
		t.Fatalf("expected no active limiters after release, got %d", len(mgr.inUse))
	}
	if got := l.limiter.Limit(); got != rate.Limit(l.rate) {
		t.Fatalf("expected limiter to reset to base rate after release: got %v want %v", got, rate.Limit(l.rate))
	}
}
