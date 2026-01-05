// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"log"
	"reflect"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

/*
Package table provides a generic Table abstraction for managing collections of entries
in a database, with built-in support for reconciliation, key management, and CRUD operations.

# Overview

The Table type is a generic structure that simplifies the implementation of strongly-typed
tables backed by a database. It provides:

- Type safety for keys and entries.
- Automatic key type registration with the underlying db.StoreCollection.
- CRUD operations (Insert, Update, Find, Delete, etc.).
- Advanced query capabilities including filtering, pagination, and sorting.
- Integration with a reconciler for event-driven updates.
- Sanity checks to prevent common mistakes (e.g., pointer types for entries or keys).

# Basic Usage

To use this library, define your key and entry types, then create a Table instance:

    type MyKey struct {
        ID string
    }
    type MyEntry struct {
        Name string
        Value int
        CreatedAt time.Time
    }

    var myTable table.Table[MyKey, MyEntry]

    // Initialize with a db.StoreCollection (e.g., from your database layer)
    err := myTable.Initialize(myCollection)
    if err != nil {
        // handle error
    }

    // Insert an entry
    entry := MyEntry{Name: "foo", Value: 42, CreatedAt: time.Now()}
    key := MyKey{ID: "abc"}
    err = myTable.Insert(ctx, &key, &entry)

    // Find an entry
    found, err := myTable.Find(ctx, &key)

    // Update an entry
    entry.Value = 100
    err = myTable.Update(ctx, &key, &entry)

    // Delete an entry
    err = myTable.DeleteKey(ctx, &key)

# FindMany with Options

Use FindManyWithOpts to query with flexible options including pagination and sorting:

    // Example 1: Simple query with limit only
    entries, err := myTable.FindManyWithOpts(ctx, nil,
        table.WithLimit(10))

    // Example 2: Sort by a single field (ascending)
    entries, err := myTable.FindManyWithOpts(ctx, nil,
        table.WithLimit(10),
        table.WithSort(table.SortOption{Field: "name", Direction: table.SortAscending}))

    // Example 3: Sort by a single field (descending)
    entries, err := myTable.FindManyWithOpts(ctx, nil,
        table.WithLimit(10),
        table.WithSort(table.SortOption{Field: "value", Direction: table.SortDescending}))

    // Example 4: Multi-field sorting
    entries, err := myTable.FindManyWithOpts(ctx, nil,
        table.WithLimit(20),
        table.WithSort(
            table.SortOption{Field: "name", Direction: table.SortAscending},
            table.SortOption{Field: "value", Direction: table.SortDescending},
        ))

    // Example 5: Pagination with offset and limit
    entries, err := myTable.FindManyWithOpts(ctx, nil,
        table.WithOffset(20),
        table.WithLimit(10))

    // Example 6: Complete example - filter, pagination, and sorting
    import "go.mongodb.org/mongo-driver/v2/bson"

    filter := bson.D{{Key: "value", Value: bson.D{{Key: "$gt", Value: 100}}}}
    entries, err := myTable.FindManyWithOpts(ctx, filter,
        table.WithOffset(20),
        table.WithLimit(10),
        table.WithSort(table.SortOption{Field: "created_at", Direction: table.SortDescending}))

    // Example 7: Query without any options (returns all entries)
    entries, err := myTable.FindManyWithOpts(ctx, filter)

    // Example 8: Backwards compatibility - use original FindMany
    entries, err := myTable.FindMany(ctx, filter, 0, 10)

# Notes

- The entry type E must NOT be a pointer type.
- The key type K must NOT be a pointer type.
- The Table must be initialized before use.
- All operations are context-aware for cancellation and timeouts.
- Sort field names should match the BSON field names in your MongoDB collection.
- Multi-field sorting applies sorts in the order specified (first sort takes precedence).
- The functional options pattern (WithLimit, WithOffset, WithSort) allows for easy extension of FindMany capabilities in the future without breaking the API.
- Options can be combined in any order and are all optional.

*/

// SortDirection represents the direction for sorting fields.
type SortDirection int

const (
	// SortAscending sorts in ascending order (1).
	SortAscending SortDirection = 1
	// SortDescending sorts in descending order (-1).
	SortDescending SortDirection = -1
)

// SortOption represents a field name and sort direction for ordering query results.
type SortOption struct {
	Field     string
	Direction SortDirection
}

// buildSortDocument converts a slice of SortOption into a bson.D document for MongoDB sorting.
func buildSortDocument(sortBy []SortOption) bson.D {
	if len(sortBy) == 0 {
		return nil
	}
	sort := bson.D{}
	for _, s := range sortBy {
		sort = append(sort, bson.E{Key: s.Field, Value: int(s.Direction)})
	}
	return sort
}

// FindOptions contains optional parameters for FindMany operations.
type FindOptions struct {
	Limit  *int32
	Offset *int32
	Sort   []SortOption
}

// FindOption is a functional option for configuring FindMany queries.
type FindOption func(*FindOptions)

// WithLimit sets the maximum number of results to return.
func WithLimit(limit int32) FindOption {
	return func(opts *FindOptions) {
		opts.Limit = &limit
	}
}

// WithOffset sets the number of results to skip before returning.
func WithOffset(offset int32) FindOption {
	return func(opts *FindOptions) {
		opts.Offset = &offset
	}
}

// WithSort sets the sort order for results. Multiple sort options can be provided.
func WithSort(sort ...SortOption) FindOption {
	return func(opts *FindOptions) {
		opts.Sort = append(opts.Sort, sort...)
	}
}

// Table is a generic table type providing common functions and types to specific
// structures each table is built using. It ensures sanity checks and provides
// common functionality for database-backed tables.
//
// K: Key type (must NOT be a pointer type, typically a struct or primitive)
// E: Entry type (must NOT be a pointer type)
type Table[K any, E any] struct {
	reconciler.ManagerImpl
	col db.StoreCollection
}

// Initialize sets up the Table with the provided db.StoreCollection.
// It performs sanity checks on the entry and key types and registers the key type with the collection.
// Must be called before any other operation.
//
// Returns an error if the table is already initialized, the entry or key type is a pointer,
// or if the collection setup fails.
func (t *Table[K, E]) Initialize(col db.StoreCollection) error {
	if t.col != nil {
		return errors.Wrapf(errors.AlreadyExists, "Table is already initialized")
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
	return nil
}

// callback is invoked on collection changes and notifies the reconciler.
func (t *Table[K, E]) callback(op string, wKey any) {
	t.NotifyCallback(wKey)
}

// keyOnly is a helper struct for extracting keys from the collection.
type keyOnly[K any] struct {
	Key K `bson:"_id,omitempty"`
}

// ReconcilerGetAllKeys returns all keys in the table.
// Used by the reconciler to enumerate all managed entries.
func (t *Table[K, E]) ReconcilerGetAllKeys() []any {
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
func (t *Table[K, E]) Insert(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.InsertOne(ctx, key, entry)
}

// Locate finds an entry by key, inserts it if it doesn't exist, or updates it if it does.
// Returns an error if the table is not initialized or the operation fails.
func (t *Table[K, E]) Locate(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.UpdateOne(ctx, key, entry, true)
}

// Update modifies an existing entry with the given key.
// Returns an error if the table is not initialized or the update fails.
func (t *Table[K, E]) Update(ctx context.Context, key *K, entry *E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.UpdateOne(ctx, key, entry, false)
}

// Find retrieves an entry by key.
// Returns the entry and error if not found or if the table is not initialized.
func (t *Table[K, E]) Find(ctx context.Context, key *K) (*E, error) {
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

// FindMany retrieves multiple entries matching the provided filter.
// Returns a slice of entries and error if none found or if the table is not initialized.
func (t *Table[K, E]) FindMany(ctx context.Context, filter any, offset, limit int32) ([]*E, error) {
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

// FindManyWithOpts retrieves multiple entries matching the provided filter with optional parameters.
// Supports pagination (limit, offset) and sorting through functional options.
// Returns a slice of entries and error if none found or if the table is not initialized.
//
// Example usage:
//
//	results, err := table.FindManyWithOpts(ctx, filter,
//	    WithLimit(10),
//	    WithOffset(20),
//	    WithSort(SortOption{Field: "price", Direction: SortAscending}))
func (t *Table[K, E]) FindManyWithOpts(ctx context.Context, filter any, opts ...FindOption) ([]*E, error) {
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

	// Execute query
	var data []*E
	err := t.col.FindMany(ctx, filter, &data, mongoOpts)
	if err != nil {
		return nil, errors.Wrapf(errors.NotFound, "failed to find any entry: %s", err)
	}

	return data, nil
}

// Count retrieves count of entries matching the provided filter.
// Returns count of entries and error if none found or if the table is not initialized.
func (t *Table[K, E]) Count(ctx context.Context, filter any) (int64, error) {
	if t.col == nil {
		return 0, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.Count(ctx, filter)
}

// DeleteByFilter deletes entries matching the provided filter.
// Returns number of entries deleted and error if any
func (t *Table[K, E]) DeleteByFilter(ctx context.Context, filter any) (int64, error) {
	if t.col == nil {
		return 0, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.DeleteMany(ctx, filter)

}

// DeleteKey removes an entry by key from the table.
// Returns an error if the table is not initialized or the delete fails.
func (t *Table[K, E]) DeleteKey(ctx context.Context, key *K) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.DeleteOne(ctx, key)
}
