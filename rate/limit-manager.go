package rate

import (
	"sync"

	"golang.org/x/time/rate"

	"github.com/go-core-stack/core/errors"
)

// LimitManager tracks the configured limiters and redistributes
// capacity when individual limiters go in or out of active use.
type LimitManager struct {
	rate      int64               // aggregate rate budget shared by all limiters
	committed int64               // sum of nominal rates requested by registered limiters
	mu        sync.Mutex          // protects concurrent access to the limiter state
	limiters  map[string]*Limiter // registry of all configured limiters
	inUse     map[string]*Limiter // subset of limiters currently marked as active
}

// SetRate updates the aggregate budget and reapportions tokens across the active limiters.
func (m *LimitManager) SetRate(rate int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rate == rate {
		return
	}
	m.rate = rate
	m.rebalanceLocked()
}

// EnsureLimiter registers a limiter if missing or updates the rate/burst of an existing one.
// When the limiter is actively in use the shared budget is rebalanced immediately.
func (m *LimitManager) EnsureLimiter(key string, r, burst int64) (*Limiter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureLimiterLocked(key, r, burst)
}

// RemoveLimiter unregisters a limiter and redistributes its share across the remaining actives.
func (m *LimitManager) RemoveLimiter(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLimiterLocked(key)
}

// UpdateInUse marks a limiter as being actively used and reapportions
// the available rate across the currently active limiters.
func (m *LimitManager) UpdateInUse(l *Limiter, use bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.limiters[l.key]; !ok {
		return
	}
	if use {
		m.inUse[l.key] = l
	} else {
		delete(m.inUse, l.key)
		l.limiter.SetLimit(rate.Limit(l.rate))
		l.limiter.SetBurst(int(l.burst))
	}
	m.rebalanceLocked()
}

// NewLimiter registers a limiter with the manager and returns it for use.
// The limiter is configured with the provided sustained rate and burst size.
func (m *LimitManager) NewLimiter(key string, r, burst int64) (*Limiter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.limiters[key]; ok {
		return nil, errors.Wrapf(errors.AlreadyExists, "limiter %q, already exists", key)
	}

	lim, err := m.ensureLimiterLocked(key, r, burst)
	if err != nil {
		return nil, err
	}
	return lim, nil
}

// NewLimitManager constructs a LimitManager with the specified aggregate rate budget.
func NewLimitManager(rate int64) *LimitManager {
	return &LimitManager{
		rate:     rate,
		limiters: make(map[string]*Limiter),
		inUse:    make(map[string]*Limiter),
	}
}

func (m *LimitManager) ensureLimiterLocked(key string, r, burst int64) (*Limiter, error) {
	if r <= 0 || burst <= 0 {
		m.removeLimiterLocked(key)
		return nil, nil
	}

	if lim, ok := m.limiters[key]; ok {
		delta := r - lim.rate
		lim.configure(r, burst)
		m.committed += delta
		if _, active := m.inUse[key]; active {
			m.rebalanceLocked()
		}
		return lim, nil
	}

	lim := &Limiter{
		mgr:     m,
		key:     key,
		rate:    r,
		burst:   burst,
		limiter: rate.NewLimiter(rate.Limit(r), int(burst)),
	}
	m.limiters[key] = lim
	// TODO(Prabhjot) handle oversubscription of committed vs total.
	m.committed += r
	return lim, nil
}

func (m *LimitManager) removeLimiterLocked(key string) {
	lim, ok := m.limiters[key]
	if !ok {
		return
	}

	delete(m.limiters, key)
	lim.limiter.SetLimit(rate.Limit(lim.rate))
	lim.limiter.SetBurst(int(lim.burst))

	delete(m.inUse, key)

	m.committed -= lim.rate
	if m.committed < 0 {
		m.committed = 0
	}

	m.rebalanceLocked()
}

func (m *LimitManager) rebalanceLocked() {
	if len(m.inUse) == 0 {
		return
	}

	if m.rate <= 0 {
		m.resetActiveLimiterLimits()
		return
	}

	var sumActive int64
	for _, lim := range m.inUse {
		sumActive += lim.rate
	}
	if sumActive <= 0 {
		m.resetActiveLimiterLimits()
		return
	}

	// Scale each limiter in proportion to its nominal rate so the shared budget
	// is fully consumed while honouring the global ceiling.
	for _, lim := range m.inUse {
		scaled := (lim.rate * m.rate) / sumActive
		if scaled < 1 {
			scaled = 1
		}
		lim.limiter.SetLimit(rate.Limit(scaled))
		lim.limiter.SetBurst(int(lim.burst))
	}
}

func (m *LimitManager) resetActiveLimiterLimits() {
	for _, lim := range m.inUse {
		lim.limiter.SetLimit(rate.Limit(0))
		lim.limiter.SetBurst(int(lim.burst))
	}
}
