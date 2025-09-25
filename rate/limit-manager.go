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

// UpdateInUse marks a limiter as being actively used and reapportions
// the available rate across the currently active limiters.
func (m *LimitManager) UpdateInUse(l *Limiter, use bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if use {
		m.inUse[l.key] = l
	} else {
		delete(m.inUse, l.key)
		l.limiter.SetLimit(rate.Limit(l.rate))
		if len(m.inUse) == 0 {
			return
		}
	}
	var sumActive int64
	for _, l := range m.inUse {
		sumActive += l.rate
	}
	// Scale each limiter in proportion to its nominal rate so that the shared
	// budget is fully consumed while still honouring the global ceiling and
	// keeping the distribution fair across participants.
	for _, l := range m.inUse {
		scaled := (l.rate * m.rate) / sumActive
		if scaled < 1 {
			scaled = 1
		}
		l.limiter.SetLimit(rate.Limit(scaled))
	}
}

// NewLimiter registers a limiter with the manager and returns it for use.
// The limiter is configured with the provided sustained rate and burst size.
func (m *LimitManager) NewLimiter(key string, r, burst int64) (*Limiter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.limiters[key]
	if ok {
		return nil, errors.Wrapf(errors.AlreadyExists, "limiter %q, already exists", key)
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

// NewLimitManager constructs a LimitManager with the specified aggregate rate budget.
func NewLimitManager(rate int64) *LimitManager {
	return &LimitManager{
		rate:     rate,
		limiters: make(map[string]*Limiter),
		inUse:    make(map[string]*Limiter),
	}
}
