// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
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

// CachedTable is a generic table type providing common functions and types to specific
// structures each table is built using. This table also ensure keeping an inmemory
// cache information to enable better responsiveness for critical path data fetch, where
// we are required to be consistent, but it is ok to let go some part of accuracy,
// assuming system will automatically converge as it settles down with change propagation.
// It also ensures sanity checks and provides common functionality for database-backed
// tables.
//
// K: Key type (must NOT be a pointer type, typically a struct or primitive)
// E: Entry type (must NOT be a pointer type)
type CachedTable[K comparable, E any] struct {
	reconciler.ManagerImpl
	cacheMu sync.RWMutex
	cache   map[K]*E
	col     db.StoreCollection
}

// Initialize sets up the Table with the provided db.StoreCollection.
// It performs sanity checks on the entry and key types and registers the key type with the collection.
// Must be called before any other operation.
//
// Returns an error if the table is already initialized, the entry or key type is a pointer,
// or if the collection setup fails.
func (t *CachedTable[K, E]) Initialize(col db.StoreCollection) error {
	if t.col != nil {
		return errors.Wrapf(errors.AlreadyExists, "Table is already initialized")
	}

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

	// Register callback for collection changes
	err = col.Watch(context.Background(), nil, t.callback)
	if err != nil {
		return err
	}

	// Initialize the reconciler manager
	err = t.ManagerImpl.Initialize(context.Background(), t)
	if err != nil {
		return err
	}

	t.col = col

	list := []keyOnly[K]{}
	err = t.col.FindMany(context.Background(), nil, &list)
	if err != nil {
		log.Panicf("got error while fetching all keys %s", err)
	}
	for _, k := range list {
		entry, err := t.DBFind(context.Background(), &k.Key)
		if err != nil {
			// this should not happen in regular scenarios
			// log and return from here
			log.Printf("failed to find an entry, got error: %s", err)
		} else {
			func() {
				t.cacheMu.Lock()
				defer t.cacheMu.Unlock()
				t.cache[k.Key] = entry
			}()
		}
	}

	return nil
}

// callback is invoked on collection changes and notifies the reconciler.
func (t *CachedTable[K, E]) callback(op string, wKey any) {
	key, ok := wKey.(*K)
	// failure should logically never happen, but lets handle just incase
	if ok {
		entry, err := t.DBFind(context.Background(), key)
		if err != nil {
			if errors.IsNotFound(err) {
				// consider delete scenario
				delete(t.cache, *key)
			} else {
				// this should not happen in regular scenarios
				// log and return from here
				log.Printf("failed to find an entry, got error: %s", err)
			}
		} else {
			func() {
				t.cacheMu.Lock()
				defer t.cacheMu.Unlock()
				t.cache[*key] = entry
			}()
		}
	}
	t.NotifyCallback(wKey)
}

// ReconcilerGetAllKeys returns all keys in the table.
// Used by the reconciler to enumerate all managed entries.
func (t *CachedTable[K, E]) ReconcilerGetAllKeys() []any {
	list := []keyOnly[K]{}
	keys := []any{}
	err := t.col.FindMany(context.Background(), nil, &list)
	if err != nil {
		log.Panicf("got error while fetching all keys %s", err)
	}
	for _, k := range list {
		keys = append(keys, &k.Key)
	}
	return []any(keys)
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

// Find retrieves an entry by key from the Cache
// Returns the entry and error if not found or if the table is not initialized.
func (t *CachedTable[K, E]) Find(ctx context.Context, key *K) (*E, error) {
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	entry, ok := t.cache[*key]
	if !ok {
		return nil, errors.Wrapf(errors.NotFound, "failed to find entry with key %v", key)
	}
	return entry, nil
}

// DBFind retrieves an entry by key from the Database
// Returns the entry and error if not found or if the table is not initialized.
func (t *CachedTable[K, E]) DBFind(ctx context.Context, key *K) (*E, error) {
	var data E
	if t.col == nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	err := t.col.FindOne(ctx, key, &data)
	if err != nil {
		return nil, errors.Wrapf(errors.NotFound, "failed to find entry with key %v: %s", key, err)
	}
	return &data, err
}

// DBFindMany retrieves multiple entries matching the provided filter from database.
// Returns a slice of entries and error if none found or if the table is not initialized.
func (t *CachedTable[K, E]) DBFindMany(ctx context.Context, filter any, offset, limit int32) ([]*E, error) {
	if t.col == nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	var data []*E
	opts := options.Find().SetLimit(int64(limit)).SetSkip(int64(offset))
	err := t.col.FindMany(ctx, filter, &data, opts)
	if err != nil {
		return nil, errors.Wrapf(errors.NotFound, "failed to find any entry: %s", err)
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
