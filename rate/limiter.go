package rate

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// Limiter wraps a token bucket rate limiter and reports usage back to the
// LimitManager so the shared capacity can be rebalanced.
type Limiter struct {
	mgr     *LimitManager
	key     string
	rate    int64
	burst   int64
	limiter *rate.Limiter
	usage   int // number of concurrent users that have marked the limiter as in-use
	mu      sync.Mutex
}

// SetInUse increments or decrements the active usage counter and notifies the
// LimitManager when the limiter transitions between idle and active states.
func (l *Limiter) SetInUse(use bool) {
	if l.mgr == nil {
		panic("limiter not initialized with manager")
	}
	l.mu.Lock()
	if use {
		l.usage++
	} else {
		l.usage--
	}
	activate := false
	notify := false
	if l.usage <= 0 {
		notify = true
	} else if l.usage == 1 {
		notify = true
		activate = true
	}
	l.mu.Unlock()
	if notify {
		l.mgr.updateInUse(l, activate)
	}
}

// WaitN acquires n tokens from the underlying rate limiter, blocking as needed.
func (l *Limiter) WaitN(ctx context.Context, n int) error {
	if l.mgr == nil {
		panic("limiter not initialized with manager")
	}
	// if mgr is not nil, then it is expected that limiter is also non-nil
	// as they are created together in LimitManager.NewLimiter.
	return l.limiter.WaitN(ctx, n)
}
