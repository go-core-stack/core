// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"cmp"
	"context"
	"log"
	"math/rand/v2"
	"reflect"
	"slices"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

// CachedTableConfig holds configuration options for CachedTable initialization.
type CachedTableConfig struct {
	// ReadThrough enables lazy loading: entries are loaded from DB on cache miss
	// instead of eagerly loading all entries at initialization.
	// Default: false (eager loading)
	ReadThrough bool

	// Filter specifies an optional filter for the initial eager load (FindMany)
	// and for ReconcilerGetAllKeys. When set, only entries matching this filter
	// are loaded into the cache and enumerated by the reconciler.
	// The filter is passed directly to StoreCollection.FindMany.
	//
	// It is also the table's scope for the typed operations that select or
	// guard rows: CountWhere, DeleteWhere and UpdateWithOpts with If AND it
	// in. Count, DeleteByFilter and DBFindManyWithOpts do not.
	// Default: nil (all entries)
	Filter any

	// WatchPipeline specifies an optional aggregation pipeline for the change
	// stream (Watch). When set, only change events matching the pipeline are
	// delivered to the cache callback.
	// The pipeline is passed directly to StoreCollection.Watch and should be
	// a mongo.Pipeline ([]bson.D) with $match stages or similar.
	// Default: nil (all change events)
	WatchPipeline any
}

// CachedTableOption is a functional option for configuring CachedTable.
type CachedTableOption func(*CachedTableConfig)

// WithReadThrough enables read-through caching mode where entries are loaded
// from the database on cache miss instead of eagerly loading all entries.
// This is useful for large datasets where loading everything upfront is impractical.
func WithReadThrough() CachedTableOption {
	return func(cfg *CachedTableConfig) {
		cfg.ReadThrough = true
	}
}

// WithFilter sets a filter for the initial eager load and ReconcilerGetAllKeys.
// Only entries matching this filter will be loaded into the cache at initialization
// and returned when the reconciler enumerates all keys.
//
// This is useful when multiple CachedTable instances share the same underlying
// MongoDB collection but each instance should only manage a subset of documents
// (e.g., filtering by a type discriminator field).
//
// The filter is also the table's scope for CountWhere, DeleteWhere and
// UpdateWithOpts with If, which AND it in. Count, DeleteByFilter and
// DBFindManyWithOpts (with or without Where) do not.
//
// The filter is passed directly to StoreCollection.FindMany and should be a
// valid MongoDB filter document (e.g., bson.M{"key.type": "slack"}).
//
// Example usage:
//
//	import "go.mongodb.org/mongo-driver/v2/bson"
//
//	err := table.InitializeWithConfig(col,
//	    WithFilter(bson.M{"key.type": "slack"}))
func WithFilter(filter any) CachedTableOption {
	return func(cfg *CachedTableConfig) {
		cfg.Filter = filter
	}
}

// WithWatchPipeline sets an aggregation pipeline for the change stream.
// Only change events matching the pipeline will be delivered to the cache
// callback, preventing the cache from receiving and processing irrelevant
// change events.
//
// This is useful when multiple CachedTable instances share the same underlying
// MongoDB collection but each instance should only react to changes for its
// own subset of documents.
//
// The pipeline is passed directly to StoreCollection.Watch and should be a
// mongo.Pipeline ([]bson.D) with appropriate $match stages.
//
// WARNING: MongoDB delete events omit the fullDocument field. If your pipeline
// filters on fullDocument fields (e.g., fullDocument.key.type), delete events
// will be silently dropped by the change stream and will NOT reach the cache
// callback. The cache relies on the reconciler to eventually evict stale entries,
// which means there will be a window of stale data until the next reconciliation
// sweep. For immediate delete propagation, use one of these approaches:
//
//   - Use fullDocumentBeforeChange (requires MongoDB 6.0+ with pre-images enabled
//     on the collection) to match deletes by the document's prior state
//   - Include documentKey-based matching if the filter field is part of the _id
//   - Accept the reconciler-based eviction delay for your use case
//
// Example with delete handling (MongoDB 6.0+ with pre-images):
//
//	pipeline := mongo.Pipeline{
//	    {{Key: "$match", Value: bson.M{"$or": bson.A{
//	        bson.M{"operationType": bson.M{"$in": bson.A{"insert", "replace", "update"}},
//	            "fullDocument.key.type": "slack"},
//	        bson.M{"operationType": "delete",
//	            "fullDocumentBeforeChange.key.type": "slack"},
//	    }}}},
//	}
//	err := table.InitializeWithConfig(col, WithWatchPipeline(pipeline))
//
// Simple example (deletes handled via reconciler):
//
//	pipeline := mongo.Pipeline{
//	    {{Key: "$match", Value: bson.M{"fullDocument.key.type": "slack"}}},
//	}
//	err := table.InitializeWithConfig(col, WithWatchPipeline(pipeline))
func WithWatchPipeline(pipeline any) CachedTableOption {
	return func(cfg *CachedTableConfig) {
		cfg.WatchPipeline = pipeline
	}
}

// CachedTable is a generic table type providing common functions and types to specific
// structures each table is built using. This table also ensure keeping an inmemory
// cache information to enable better responsiveness for critical path data fetch, where
// we are required to be consistent, but it is ok to let go some part of accuracy,
// assuming system will automatically converge as it settles down with change propagation.
// It also ensures sanity checks and provides common functionality for database-backed
// tables.
//
// # Cache staleness contract
//
// The cache is refreshed from change-stream events via callback. When a change
// event is observed, callback re-reads the authoritative row via DBFind and
// updates the cache. If that re-read fails with anything other than a genuine
// NotFound, callback deliberately RETAINS the existing cached value instead of
// evicting it, to avoid surfacing a spurious miss on a momentary DB blip, and
// records the key as pending a refresh.
//
// Each table runs one retrier beside the change stream, which reads every
// pending key again, with backoff and without giving up, until a read
// succeeds. It applies that read as callback does, storing the row or
// evicting it on NotFound, and then notifies the reconciler again, so
// reconcilers run on the settled value. A newer event for the key always wins
// over an older retry. As a consequence, after a failed refresh the cache may
// knowingly hold a stale entry for as long as reads of that row keep failing,
// but no longer: a row that is written rarely does not wait for its next
// change to be read again. Keys still pending when the collection's client is
// closed stay stale; after a restart the eager load reads every row, while a
// read-through cache starts empty. In read-through mode a key that is not
// cached is not recorded, since the next Find reads the database anyway.
//
// Until the retry succeeds, Find(key) returns the retained value, and the
// reconcilers notified by the failed event run on it. A reconciler that
// requires authoritative, up-to-date state at that moment MUST call
// DBFind(key) explicitly rather than relying on Find(key); one that reads via
// Find(key) is run again once the retry has settled the key.
//
// K: Key type (must NOT be a pointer type, typically a struct or primitive)
// E: Entry type (must NOT be a pointer type)
type CachedTable[K comparable, E any] struct {
	reconciler.ManagerImpl
	cacheMu       sync.RWMutex
	cache         map[K]*E
	loadTouched   map[K]struct{}       // keys the callback decided during the eager load; guarded by cacheMu
	pending       map[K]pendingRefresh // keys whose refresh failed; guarded by cacheMu
	pendingGen    uint64               // last generation given to a pending key, only increases; guarded by cacheMu
	emptied       uint64               // times the pending set has emptied; guarded by cacheMu
	wake          chan struct{}        // one slot; recording a pending key wakes the retrier
	col           db.StoreCollection
	readThrough   bool
	filter        any // optional filter for FindMany (eager load + reconciler)
	watchPipeline any // optional pipeline for Watch (change stream)
}

// Initialize sets up the Table with the provided db.StoreCollection using default configuration.
// By default, it eagerly loads all entries from the database into the cache.
// For read-through caching, use InitializeWithConfig with WithReadThrough option.
//
// Returns an error if the table is already initialized, the entry or key type is a pointer,
// or if the collection setup or the eager load fails; see InitializeWithConfig.
func (t *CachedTable[K, E]) Initialize(col db.StoreCollection) error {
	return t.InitializeWithConfig(col)
}

// InitializeWithConfig sets up the Table with the provided db.StoreCollection and configuration options.
// It performs sanity checks on the entry and key types and registers the key type with the collection.
// Must be called before any other operation.
//
// Example usage:
//
//	// Eager loading (default)
//	err := table.InitializeWithConfig(col)
//
//	// Read-through caching
//	err := table.InitializeWithConfig(col, WithReadThrough())
//
//	// Filtered eager loading (only load entries matching filter)
//	// NOTE: For scoped isolation (e.g., type-discriminated caches sharing a
//	// collection), both WithFilter and WithWatchPipeline should typically be
//	// set together to keep the cache scope consistent. WithFilter scopes the
//	// initial load and reconciler, while WithWatchPipeline scopes the change
//	// stream. Using only one may cause cache drift — see each option's
//	// documentation for details on partial configuration behavior.
//	err := table.InitializeWithConfig(col,
//	    WithFilter(bson.M{"key.type": "slack"}),
//	    WithWatchPipeline(mongo.Pipeline{
//	        {{Key: "$match", Value: bson.M{"fullDocument.key.type": "slack"}}},
//	    }))
//
// Returns an error if the table is already initialized, the entry or key type is a pointer,
// or if the collection setup fails. With eager loading it also returns an error
// if the keys cannot be listed or an entry cannot be read, other than one deleted
// after the keys were listed: the table does not start with a partial cache.
// A table whose initialization failed is left uninitialized, with its change
// stream ended, and may be initialized again.
func (t *CachedTable[K, E]) InitializeWithConfig(col db.StoreCollection, opts ...CachedTableOption) error {
	if t.col != nil {
		return errors.Wrapf(errors.AlreadyExists, "Table is already initialized")
	}

	// Apply configuration options
	config := &CachedTableConfig{
		ReadThrough:   false, // Default to eager loading
		Filter:        nil,   // Default to all entries
		WatchPipeline: nil,   // Default to all change events
	}
	for _, opt := range opts {
		opt(config)
	}
	t.readThrough = config.ReadThrough
	t.filter = config.Filter
	t.watchPipeline = config.WatchPipeline

	if t.cache == nil {
		t.cache = map[K]*E{}
	}
	if t.pending == nil {
		t.pending = map[K]pendingRefresh{}
	}
	if t.wake == nil {
		t.wake = make(chan struct{}, 1)
	}

	var e E
	if reflect.TypeOf(e).Kind() == reflect.Pointer {
		return errors.Wrapf(errors.InvalidArgument, "Table entry type must not be a pointer")
	}

	var k K
	if reflect.TypeOf(k).Kind() == reflect.Pointer {
		return errors.Wrapf(errors.InvalidArgument, "Table key type must not be a pointer")
	}

	err := col.SetKeyType(reflect.PointerTo(reflect.TypeOf(k)))
	if err != nil {
		return err
	}

	// Preflight validation: if a filter is configured, validate it by running
	// a lightweight Count before proceeding. This catches invalid filters at
	// init time with a proper error return instead of deferring to the panic path
	// in eager load or ReconcilerGetAllKeys.
	// Count validates the filter server-side without fetching or decoding documents.
	if t.filter != nil {
		if _, err := col.Count(context.Background(), t.filter); err != nil {
			return errors.Wrapf(errors.InvalidArgument, "WithFilter: filter validation failed: %s", err)
		}
	}

	// Register callback for collection changes, using watch pipeline if
	// configured. The change stream is opened before the eager load, so a
	// change made while the load runs still reaches the cache.
	//
	// If starting fails from here on, the stream is ended and the table is
	// left uninitialized, so nothing of the failed start keeps running and
	// Initialize can be called again. stopped is set under stopMu, so once
	// it is set no callback is running or will run, and the table's fields
	// are reset without racing one.
	watchCtx, endWatch := context.WithCancel(context.Background())
	var stopMu sync.RWMutex
	stopped := false
	started := false
	defer func() {
		if started {
			return
		}
		stopMu.Lock()
		stopped = true
		stopMu.Unlock()
		endWatch()
		t.col = nil
		t.cache = map[K]*E{}
		t.loadTouched = nil
		// no retrier has started, and a key recorded during the load
		// belongs to the discarded cache
		t.pending = map[K]pendingRefresh{}
	}()

	// set before the stream starts, so the callback reads it without a
	// race
	t.col = col

	err = col.Watch(watchCtx, t.watchPipeline, func(op string, wKey any) {
		stopMu.RLock()
		defer stopMu.RUnlock()
		if !stopped {
			t.callback(op, wKey)
		}
	})
	if err != nil {
		return err
	}

	err = t.start()
	if err != nil {
		return err
	}

	// The retrier starts only once the table has started, so a failed
	// start leaves none running, and a key recorded during the eager load
	// is already pending when it does. It ends with the client, so none is
	// started for a client already closed.
	lifetime := clientLifetime(col)
	if lifetime.Err() == nil {
		go t.retryRefreshes(lifetime, refreshRetry)
	}

	started = true
	return nil
}

// start completes InitializeWithConfig once the change stream is open: it
// eagerly loads the cache unless the table is read-through, then
// initializes the reconciler manager. The manager is initialized last, so
// a failed load leaves it untouched for the next attempt.
func (t *CachedTable[K, E]) start() error {
	// The field index conditions are checked against; building it never
	// fails, so it cannot stop a table from starting.
	indexFor(reflect.TypeOf((*E)(nil)).Elem())

	// Only eagerly load entries if read-through is disabled
	if !t.readThrough {
		// The change stream is open, so the callback may store or evict
		// a key while the load runs. The load's read of that key may
		// predate the change, and writing it afterwards would put back a
		// value the callback has replaced, with no later event to correct
		// it. The callback reads the row after each change it is told
		// of, so what it decides is current or will be replaced by its
		// next event; the load must not overwrite it.
		//
		// So the callback records each key it stores or evicts in
		// loadTouched, and the load skips those. The callback's
		// mark-and-write and the load's check-and-write are each done
		// under cacheMu, so a callback either lands before the load's
		// check and is seen, or after the load's write and replaces it;
		// there is no window between them. A callback that wrote before
		// loadTouched was set read the row before the load does, so the
		// load's value is the newer one there. A callback whose read
		// fails decides nothing and marks nothing, so the load's value
		// stands, stale at worst; the key is recorded as pending, and
		// the retrier reads it again once the table has started.
		func() {
			t.cacheMu.Lock()
			defer t.cacheMu.Unlock()
			t.loadTouched = map[K]struct{}{}
		}()
		defer func() {
			t.cacheMu.Lock()
			defer t.cacheMu.Unlock()
			t.loadTouched = nil
		}()

		list := []keyOnly[K]{}
		err := t.col.FindMany(context.Background(), t.filter, &list)
		if err != nil {
			return preserveErrClass(err, "failed to eager-load keys")
		}
		for _, k := range list {
			entry, err := t.DBFind(context.Background(), &k.Key)
			if err != nil {
				if errors.IsNotFound(err) {
					// the entry was deleted after the keys were
					// listed, so it is rightly absent from the cache
					continue
				}
				// A missing entry reads as absent, not as unread,
				// and only a change to it would load it later, so
				// the table does not start with a partial cache.
				return errors.WrapErrf(errors.GetErrCode(err), err, "failed to eager-load entry with key %v", k.Key)
			}
			func() {
				t.cacheMu.Lock()
				defer t.cacheMu.Unlock()
				if _, ok := t.loadTouched[k.Key]; ok {
					// the callback has already decided this
					// key from a newer read
					return
				}
				t.cache[k.Key] = entry
			}()
		}
	}

	// Initialize the reconciler manager, which ends when the
	// collection's client is closed
	return t.ManagerImpl.Initialize(clientLifetime(t.col), t)
}

// callback is invoked on collection changes and notifies the reconciler.
//
// On each change event it re-reads the authoritative row via DBFind and
// reconciles the cache. See the CachedTable type documentation for the cache
// staleness contract: on any failure other than NotFound the existing cached
// value is retained rather than evicted, and the key is recorded for the
// table's retrier, which reads it again until a read succeeds. callback runs
// on the change-stream goroutine, so it never waits for that: every later
// event on the table would wait behind it.
func (t *CachedTable[K, E]) callback(op string, wKey any) {
	key, ok := wKey.(*K)
	// failure should logically never happen, but lets handle just incase
	if ok {
		if _, err := t.readAndApply(context.Background(), key, 0); err != nil {
			// Not evicted: dropping a live cache row on a transient
			// blip would surface a spurious miss. Every such error is
			// retried, not only Unavailable: a primary stepping down
			// or a cleared pool arrive as Unknown, and waiting is what
			// fixes them. A document that does not decode is retried
			// too, and heals once it is fixed.
			t.recordPending(*key, err)
		}
	}
	t.NotifyCallback(wKey)
}

// retryBounds bound the retry of a refresh that failed on a change event.
type retryBounds struct {
	// the wait after a failed round starts here, and starts here again
	// once a round settles a key or the set empties
	minDelay time.Duration
	// the wait doubles after each failed round up to this
	maxDelay time.Duration
	// each retry read ends after this; without a socket timeout, a
	// connection whose server never replies would hang it for good
	readTimeout time.Duration
}

// refreshRetry holds the bounds. They are not options: a table has no
// reason to wait for its database differently. A retrier copies them when
// it starts; this is a variable only so the package's tests can shorten
// them.
var refreshRetry = retryBounds{
	minDelay:    time.Second,
	maxDelay:    30 * time.Second,
	readTimeout: 30 * time.Second,
}

// pendingRefresh is a pending key's place in the retry order.
type pendingRefresh struct {
	// taken from the table-wide counter each time the key is recorded
	// or requeued
	gen uint64
	// set once a retry of the key has failed
	tried bool
}

// readAndApply reads key's row and applies it to the cache: a row read is
// stored and NotFound evicts it. Either way the key is settled: it leaves
// the pending set and is marked as decided for the eager load. Any other
// failure is returned and nothing is applied. The callback and the retrier
// both go through it, so both apply a read the same way.
//
// gen is 0 for a change event, which applies unconditionally: it read the
// row after its event's commit, so its value is current. A retry passes
// the generation the key was pending under when its read began, and its
// read is applied only if the key is still pending under it. Otherwise an
// event has settled or recorded the key since, and that event's read is
// the newer one; the retry's read is dropped and false is returned.
func (t *CachedTable[K, E]) readAndApply(ctx context.Context, key *K, gen uint64) (bool, error) {
	entry, err := t.DBFind(ctx, key)
	if err != nil && !errors.IsNotFound(err) {
		return false, err
	}
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	p, pending := t.pending[*key]
	if gen != 0 && (!pending || p.gen != gen) {
		return false, nil
	}
	if err != nil {
		// Definitive miss: the row is genuinely absent (delete
		// scenario). Safe to evict from the cache.
		delete(t.cache, *key)
	} else {
		t.cache[*key] = entry
	}
	if pending {
		delete(t.pending, *key)
		if len(t.pending) == 0 {
			t.emptied++
		}
	}
	t.markLoadTouched(*key)
	return true, nil
}

// recordPending records key, whose refresh failed with err, for the
// retrier and wakes it. A key already pending keeps its one entry and
// takes the next generation, so a retry already reading it is dropped.
// The counter is table-wide, so a key settled and recorded again never
// takes a generation an older retry read under.
//
// In read-through mode a key that is not cached is not recorded: the next
// Find reads it from the database anyway. The check and the record are
// made under one cacheMu section, and Find populates under cacheMu, so a
// populate cannot slip between them.
func (t *CachedTable[K, E]) recordPending(key K, err error) {
	recorded, newly := func() (bool, bool) {
		t.cacheMu.Lock()
		defer t.cacheMu.Unlock()
		if t.readThrough {
			if _, ok := t.cache[key]; !ok {
				return false, false
			}
		}
		p, was := t.pending[key]
		t.pendingGen++
		t.pending[key] = pendingRefresh{gen: t.pendingGen, tried: p.tried}
		return true, !was
	}()
	if !recorded {
		// neither recorded nor logged: no stale value is held
		return
	}
	if newly {
		// logged once when the key becomes pending, not per failed event
		log.Printf("failed to refresh entry with key %v on change event, retaining cached value and retrying, got error: %s", key, err)
	}
	// never blocks: a wake already waiting covers this key too
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// retryRefreshes reads each pending key again until a read of it
// succeeds, and ends with ctx, the lifetime of the collection's client.
// It runs beside the change stream, so a read that keeps failing never
// holds up later events, and it does not give up: the key that matters is
// one with no later event to refresh it.
//
// It is idle while nothing is pending, and recording a key wakes it. After
// a failed round it waits, from the minimum delay doubling to the cap.
// Keys that keep failing while the database is down do not shorten the
// wait; only a set that has emptied since the wait began does, since the
// retrier is then idle and a key recorded after that is new work. The
// delay starts again from the minimum once a round settles a key or the
// set has emptied since the last wait.
func (t *CachedTable[K, E]) retryRefreshes(ctx context.Context, b retryBounds) {
	delay := b.minDelay
	seen := t.emptiedCount()
	for {
		if t.pendingCount() == 0 {
			select {
			case <-ctx.Done():
				return
			case <-t.wake:
			}
			continue
		}
		settled, err := t.retryRound(ctx, b.readTimeout)
		if ctx.Err() != nil {
			// the client is closing: what is still pending ends
			// with it, without a line per key
			return
		}
		if settled > 0 {
			log.Printf("refreshed %d entries whose read had failed on a change event", settled)
			delay = b.minDelay
		}
		if err == nil {
			continue
		}
		if e := t.emptiedCount(); e != seen {
			// the retrier has been idle since its last wait
			seen, delay = e, b.minDelay
		}
		if t.pendingCount() == 0 {
			// an event settled the key whose read failed
			continue
		}
		wait := jitter(delay)
		log.Printf("failed to refresh %d pending entries, retrying in %s, got error: %s",
			t.pendingCount(), wait.Round(time.Millisecond), err)
		if !t.backoff(ctx, wait, seen) {
			return
		}
		delay = min(2*delay, b.maxDelay)
	}
}

// backoff waits for wait, and returns false if ctx ends first. A wake cuts
// the wait short only if the set has emptied since mark; otherwise it is
// for a key that will be read after the wait anyway.
func (t *CachedTable[K, E]) backoff(ctx context.Context, wait time.Duration, mark uint64) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-t.wake:
			if t.emptiedCount() != mark {
				return true
			}
		}
	}
}

// retryRound reads the keys pending when it starts, keys not yet tried
// first and then in the order they were recorded, and returns how many it
// settled and the error of a failed read. It ends on its first failed
// read, so a database that is down sees one read per wait, not one per
// pending key, and the failed key moves behind the others, so a key that
// keeps failing cannot starve them. cacheMu is held only to look a key up
// or apply its read, never across the read or the notification.
func (t *CachedTable[K, E]) retryRound(ctx context.Context, timeout time.Duration) (int, error) {
	settled := 0
	for _, key := range t.pendingKeys() {
		gen, ok := t.pendingGeneration(key)
		if !ok {
			// an event has settled it since the round began
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, timeout)
		applied, err := t.readAndApply(readCtx, &key, gen)
		cancel()
		if ctx.Err() != nil {
			return settled, nil
		}
		if err != nil {
			t.requeuePending(key, gen)
			return settled, err
		}
		if applied {
			settled++
			// reconcilers notified by the failed event ran on the
			// retained value, so they run again on the settled one;
			// key is this iteration's own, so the pointer is fresh
			t.NotifyCallback(&key)
		}
	}
	return settled, nil
}

// pendingCount returns the number of keys pending a refresh.
func (t *CachedTable[K, E]) pendingCount() int {
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	return len(t.pending)
}

// emptiedCount returns how many times the pending set has emptied.
func (t *CachedTable[K, E]) emptiedCount() uint64 {
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	return t.emptied
}

// pendingKeys returns the pending keys in the order a round reads them:
// keys no retry has tried yet first, so a key recorded during a wait is
// read in the next round, and then by generation, the order in which the
// keys were recorded or requeued.
func (t *CachedTable[K, E]) pendingKeys() []K {
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	keys := make([]K, 0, len(t.pending))
	for k := range t.pending {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b K) int {
		pa, pb := t.pending[a], t.pending[b]
		if pa.tried != pb.tried {
			if pb.tried {
				return -1
			}
			return 1
		}
		return cmp.Compare(pa.gen, pb.gen)
	})
	return keys
}

// pendingGeneration returns the generation key is pending under, and
// false if it is not pending.
func (t *CachedTable[K, E]) pendingGeneration(key K) (uint64, bool) {
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	p, ok := t.pending[key]
	return p.gen, ok
}

// requeuePending moves key, whose retry under gen failed, behind the
// other keys: it is marked as tried and takes the next generation. A key
// an event has settled or recorded again since the read began is left as
// it is.
func (t *CachedTable[K, E]) requeuePending(key K, gen uint64) {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	if p, ok := t.pending[key]; ok && p.gen == gen {
		t.pendingGen++
		t.pending[key] = pendingRefresh{gen: t.pendingGen, tried: true}
	}
}

// jitter spreads a wait of d over its upper half, so processes recovering
// together do not all read at once.
func jitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2+1)
}

// markLoadTouched records, while the eager load runs, that the callback
// has decided key, so the load does not overwrite it. The caller holds
// cacheMu.
func (t *CachedTable[K, E]) markLoadTouched(key K) {
	if t.loadTouched != nil {
		t.loadTouched[key] = struct{}{}
	}
}

// ReconcilerGetAllKeys returns all keys in the table, read from the
// database. If a filter was configured via WithFilter, only keys matching
// the filter are returned. It panics if the keys cannot be read.
func (t *CachedTable[K, E]) ReconcilerGetAllKeys() []any {
	keys, err := t.ReconcilerListKeys(context.Background())
	if err != nil {
		log.Panicf("got error while fetching all keys %s", err)
	}
	return keys
}

// ReconcilerListKeys returns all keys in the table, read from the
// database under ctx, filtered as ReconcilerGetAllKeys is. The reconciler
// uses it to replay existing entries to a newly registered controller,
// so the read ends when the reconciler does.
func (t *CachedTable[K, E]) ReconcilerListKeys(ctx context.Context) ([]any, error) {
	list := []keyOnly[K]{}
	keys := []any{}
	err := t.col.FindMany(ctx, t.filter, &list)
	if err != nil {
		return nil, err
	}
	for _, k := range list {
		keys = append(keys, &k.Key)
	}
	return keys, nil
}

// Insert adds a new entry to the table with the given key.
// Returns an error if the table is not initialized or the insert fails.
func (t *CachedTable[K, E]) Insert(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.InsertOne(ctx, key, entry)
}

// Locate finds an entry by key, inserts it if it doesn't exist, or updates it if it does.
// Returns an error if the table is not initialized or the operation fails.
func (t *CachedTable[K, E]) Locate(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.UpdateOne(ctx, key, entry, true)
}

// Update modifies an existing entry with the given key.
// Returns an error if the table is not initialized or the update fails.
func (t *CachedTable[K, E]) Update(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.UpdateOne(ctx, key, entry, false)
}

// Find retrieves an entry by key from the Cache.
// If read-through caching is enabled and the entry is not in cache,
// it will load the entry from the database and populate the cache.
//
// Note on staleness: Find serves whatever value is currently cached. After a
// refresh failure in callback, that value may be a knowingly retained stale
// entry until the table's retrier reads the row successfully (see the
// CachedTable type documentation and callback). A caller that requires
// authoritative, up-to-date state at that moment must call DBFind directly;
// once the retry has settled the key, Find returns the value it read and the
// reconcilers are notified again.
//
// Returns the entry and error if not found or if the table is not initialized.
func (t *CachedTable[K, E]) Find(ctx context.Context, key *K) (*E, error) {
	// First, try to find in cache with read lock
	t.cacheMu.RLock()
	entry, ok := t.cache[*key]
	t.cacheMu.RUnlock()

	if ok {
		return entry, nil
	}

	// Cache miss - handle based on read-through setting
	if !t.readThrough {
		return nil, errors.Wrapf(errors.NotFound, "failed to find entry with key %v", key)
	}

	// Read-through mode: load from database and populate cache
	// Acquire write lock to populate cache
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()

	// Double-check: another goroutine might have loaded it while we were waiting for the lock
	if entry, ok := t.cache[*key]; ok {
		return entry, nil
	}

	// Load from database
	dbEntry, err := t.DBFind(ctx, key)
	if err != nil {
		return nil, err
	}

	// Populate cache
	t.cache[*key] = dbEntry
	return dbEntry, nil
}

// DBFind retrieves an entry by key from the Database.
//
// DBFind preserves the error class returned by the db layer instead of
// flattening every failure to NotFound. The db layer classifies failures as
// NotFound (document genuinely absent), Unavailable (transient/infrastructure
// failure such as an unreachable server or timeout), AlreadyExists, etc.
// Recognized classes are propagated as-is so callers can distinguish a real
// miss from a transient blip; only genuinely unknown errors are wrapped, and
// they are wrapped as Unknown (never NotFound) so a non-miss failure can never
// masquerade as a missing row.
//
// Returns the entry and error if not found, on a backend failure, or if the
// table is not initialized.
func (t *CachedTable[K, E]) DBFind(ctx context.Context, key *K) (*E, error) {
	var data E
	if t.col == nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	err := t.col.FindOne(ctx, key, &data)
	if err != nil {
		return nil, preserveErrClass(err, "failed to find entry with key %v", key)
	}
	return &data, nil
}

// DBFindMany retrieves multiple entries matching the provided filter from database.
//
// Like DBFind, it preserves the error class from the db layer. The underlying
// cursor decode returns an empty slice (not an error) when the filter matches
// no documents, so a non-nil error here always denotes a genuine backend
// failure - never an empty result. Recognized classes (Unavailable,
// InvalidArgument, ...) are propagated as-is; only unclassified errors are
// wrapped, as Unknown.
//
// Returns a slice of entries and error on a backend failure or if the table is
// not initialized.
func (t *CachedTable[K, E]) DBFindMany(ctx context.Context, filter any, offset, limit int32) ([]*E, error) {
	if t.col == nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	var data []*E
	opts := options.Find().SetLimit(int64(limit)).SetSkip(int64(offset))
	err := t.col.FindMany(ctx, filter, &data, opts)
	if err != nil {
		return nil, preserveErrClass(err, "failed to find entries")
	}

	return data, nil
}

// DBFindManyWithOpts retrieves multiple entries matching the provided filter from database with optional parameters.
// Supports pagination (limit, offset) and sorting through functional options.
//
// Like DBFindMany, it preserves the error class from the db layer and returns
// an empty slice (not an error) when the filter matches no documents.
//
// Example usage:
//
//	results, err := table.DBFindManyWithOpts(ctx, filter,
//	    WithLimit(10),
//	    WithOffset(20),
//	    WithSort(SortOption{Field: "price", Direction: SortAscending}))
func (t *CachedTable[K, E]) DBFindManyWithOpts(ctx context.Context, filter any, opts ...FindOption) ([]*E, error) {
	if t.col == nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}

	// Apply functional options
	findOpts := &FindOptions{}
	for _, opt := range opts {
		opt(findOpts)
	}

	// Build MongoDB options
	mongoOpts := options.Find()
	if findOpts.Limit != nil {
		mongoOpts = mongoOpts.SetLimit(int64(*findOpts.Limit))
	}
	if findOpts.Offset != nil {
		mongoOpts = mongoOpts.SetSkip(int64(*findOpts.Offset))
	}
	if len(findOpts.Sort) > 0 {
		mongoOpts = mongoOpts.SetSort(buildSortDocument(findOpts.Sort))
	}

	// The table's configured filter is not added here, with or without a
	// Where: a find keeps returning what it returned before Where existed.
	// Add the scope as a Raw condition to narrow a find to it.
	filter, err := findFilter[E](findOpts, filter)
	if err != nil {
		return nil, err
	}

	// Execute query
	var data []*E
	err = t.col.FindMany(ctx, filter, &data, mongoOpts)
	if err != nil {
		return nil, preserveErrClass(err, "failed to find entries")
	}

	return data, nil
}

// Count retrieves count of entries matching the provided filter.
// Returns count of entries and error if none found or if the table is not initialized.
func (t *CachedTable[K, E]) Count(ctx context.Context, filter any) (int64, error) {
	if t.col == nil {
		return 0, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.Count(ctx, filter)
}

// DeleteByFilter deletes entries matching the provided filter.
// Returns number of entries deleted and error if any
func (t *CachedTable[K, E]) DeleteByFilter(ctx context.Context, filter any) (int64, error) {
	if t.col == nil {
		return 0, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.DeleteMany(ctx, filter)

}

// DeleteKey removes an entry by key from the table.
// Returns an error if the table is not initialized or the delete fails.
func (t *CachedTable[K, E]) DeleteKey(ctx context.Context, key *K) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.DeleteOne(ctx, key)
}
