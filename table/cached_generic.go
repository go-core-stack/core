// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"log"
	"reflect"
	"sync"

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
// updates the cache. If that re-read fails with a transient/infrastructure
// error (Unavailable) - rather than a genuine NotFound - callback deliberately
// RETAINS the existing cached value instead of evicting it, to avoid surfacing
// a spurious miss on a momentary DB blip. As a consequence, after a transient
// failure the cache may knowingly hold a stale entry until the change stream
// delivers a subsequent event for that key.
//
// Convergence is driven by the reconciler, which requeues and re-runs
// Reconcile(key) - it does NOT re-run callback or DBFind on its own. Therefore
// a reconciler that reads via Find(key) will keep observing the same retained
// (possibly stale) value across retries and will not converge on its own. A
// reconciler that requires authoritative, up-to-date state after a transient
// failure MUST call DBFind(key) explicitly rather than relying on Find(key).
//
// K: Key type (must NOT be a pointer type, typically a struct or primitive)
// E: Entry type (must NOT be a pointer type)
type CachedTable[K comparable, E any] struct {
	reconciler.ManagerImpl
	cacheMu       sync.RWMutex
	cache         map[K]*E
	loadTouched   map[K]struct{} // keys the callback decided during the eager load; guarded by cacheMu
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
		// fails transiently decides nothing and marks nothing, so the
		// load's value stands, stale at worst, as the staleness contract
		// on CachedTable allows.
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
// staleness contract: on a transient (Unavailable) or unknown failure the
// existing cached value is retained rather than evicted, so the cache may
// knowingly hold a stale entry until a later event arrives; reconcilers that
// need authoritative state must use DBFind rather than Find.
func (t *CachedTable[K, E]) callback(op string, wKey any) {
	key, ok := wKey.(*K)
	// failure should logically never happen, but lets handle just incase
	if ok {
		entry, err := t.DBFind(context.Background(), key)
		if err != nil {
			if errors.IsNotFound(err) {
				// Definitive miss: the row is genuinely absent (delete
				// scenario). Safe to evict from the cache.
				func() {
					t.cacheMu.Lock()
					defer t.cacheMu.Unlock()
					delete(t.cache, *key)
					t.markLoadTouched(*key)
				}()
			} else {
				// Transient (Unavailable) or unknown failure: the DB could
				// not be consulted authoritatively. Do NOT evict - dropping a
				// live cache row on a transient blip would surface a spurious
				// miss until the next reconciler sweep. Retain the existing
				// cached value and let the reconciler converge.
				log.Printf("failed to refresh entry on change event, retaining cached value, got error: %s", err)
			}
		} else {
			func() {
				t.cacheMu.Lock()
				defer t.cacheMu.Unlock()
				t.cache[*key] = entry
				t.markLoadTouched(*key)
			}()
		}
	}
	t.NotifyCallback(wKey)
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
// transient (Unavailable) refresh failure in callback, that value may be a
// knowingly retained stale entry (see the CachedTable type documentation and
// callback). A caller that requires authoritative, up-to-date state after a
// suspected transient failure must call DBFind directly - the reconciler's
// requeue re-runs Reconcile(key), not callback/DBFind, so repeated Find calls
// will keep returning the same retained value.
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
