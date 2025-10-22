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

func TestLimitManagerEnsureLimiterUpdatesExisting(t *testing.T) {
	mgr := NewLimitManager(100)

	alpha, err := mgr.NewLimiter("alpha", 30, 10)
	if err != nil {
		t.Fatalf("unexpected error creating limiter alpha: %v", err)
	}

	if _, err := mgr.EnsureLimiter("alpha", 20, 8); err != nil {
		t.Fatalf("EnsureLimiter update failed: %v", err)
	}
	if alpha.rate != 20 {
		t.Fatalf("expected alpha rate to update to 20, got %d", alpha.rate)
	}
	if got := alpha.limiter.Limit(); got != rate.Limit(20) {
		t.Fatalf("expected alpha limiter limit 20, got %v", got)
	}

	beta, err := mgr.NewLimiter("beta", 40, 12)
	if err != nil {
		t.Fatalf("unexpected error creating limiter beta: %v", err)
	}

	alpha.SetInUse(true)
	beta.SetInUse(true)

	if _, err := mgr.EnsureLimiter("alpha", 60, 15); err != nil {
		t.Fatalf("EnsureLimiter rebalance failed: %v", err)
	}
	if got := alpha.limiter.Limit(); got != rate.Limit(60) {
		t.Fatalf("expected alpha scaled limit 60, got %v", got)
	}
	if got := beta.limiter.Limit(); got != rate.Limit(40) {
		t.Fatalf("expected beta scaled limit 40, got %v", got)
	}

	alpha.SetInUse(false)
	beta.SetInUse(false)
	if got := alpha.limiter.Limit(); got != rate.Limit(alpha.rate) {
		t.Fatalf("expected alpha to reset to base rate, got %v", got)
	}
}

func TestLimitManagerSetRateAndRemoveLimiter(t *testing.T) {
	mgr := NewLimitManager(100)

	alpha, err := mgr.NewLimiter("alpha", 30, 10)
	if err != nil {
		t.Fatalf("unexpected error creating limiter alpha: %v", err)
	}
	beta, err := mgr.NewLimiter("beta", 70, 20)
	if err != nil {
		t.Fatalf("unexpected error creating limiter beta: %v", err)
	}

	alpha.SetInUse(true)
	beta.SetInUse(true)

	mgr.SetRate(200)
	if got := alpha.limiter.Limit(); got != rate.Limit((30*200)/100) {
		t.Fatalf("expected alpha limit 60 after SetRate, got %v", got)
	}
	if got := beta.limiter.Limit(); got != rate.Limit((70*200)/100) {
		t.Fatalf("expected beta limit 140 after SetRate, got %v", got)
	}

	mgr.RemoveLimiter("alpha")
	if _, ok := mgr.limiters["alpha"]; ok {
		t.Fatalf("expected alpha limiter to be removed")
	}
	if got := len(mgr.inUse); got != 1 {
		t.Fatalf("expected 1 active limiter after removal, got %d", got)
	}
	if got := beta.limiter.Limit(); got != rate.Limit(200) {
		t.Fatalf("expected beta to inherit full capacity, got %v", got)
	}
	if got := alpha.limiter.Limit(); got != rate.Limit(alpha.rate) {
		t.Fatalf("expected alpha to retain base limit after removal, got %v", got)
	}

	beta.SetInUse(false)
}
