// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"reflect"
	"time"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
)

// Entry is an ephemeral table entry written by this process; Close deletes it.
type Entry interface {
	Close() error
}

// ephemeralData is the stored document beside its key (_id).
type ephemeralData[M any] struct {
	CreateTime int64  `bson:"createTime,omitempty"`
	Owner      string `bson:"owner,omitempty"`
	Meta       *M     `bson:"meta,omitempty"`
}

type ephemeralEntry[K any] struct {
	key   *K
	owner string
	col   db.StoreCollection
}

// Close deletes the entry only while it is still this process's (see
// releaseOwned): a Close arriving after owner cleanup has removed the entry
// cannot delete one another process has since created under the same key. It
// returns NotFound when there is nothing of this process's left to delete.
func (e *ephemeralEntry[K]) Close() error {
	return releaseOwned(e.col, e.key, e.owner)
}

// EphemeralTable holds entries that live as long as the process that wrote
// them, each carrying a meta value of type M. When the owner leaves the
// owner-table, and at start for owners no longer present, its entries are
// deleted, exactly as for locks.
type EphemeralTable[K any, M any] struct {
	// collection name hosting the entries
	colName string

	// collection object for the database store
	col db.StoreCollection

	// context in which this table works
	ctx context.Context

	// Context cancel function
	cancelFn context.CancelFunc

	// owner cleanup for the entries in col
	owned *ownedEntries
}

// callback handles entry events: a delete needs nothing, anything else checks
// that the entry's owner is still present.
func (t *EphemeralTable[K, M]) callback(op string, wKey any) {
	if op == "delete" {
		return
	}
	t.owned.checkOwner(wKey)
}

// Create writes an entry owned by this process; it fails with AlreadyExists
// if the key exists.
func (t *EphemeralTable[K, M]) Create(ctx context.Context, key *K, meta *M) (Entry, error) {
	// if ownertable is not initialized, then entries cannot be owned
	if ownerTable == nil || ownerTable.key == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "owner infra for ephemeral table is not initialized")
	}

	data := &ephemeralData[M]{
		CreateTime: time.Now().Unix(),
		Owner:      ownerTable.key.Name,
		Meta:       meta,
	}

	err := t.col.InsertOne(ctx, key, data)
	if err != nil {
		return nil, err
	}

	return &ephemeralEntry[K]{
		key:   key,
		owner: data.Owner,
		col:   t.col,
	}, nil
}

// Find returns the entry's meta value; NotFound if there is none.
func (t *EphemeralTable[K, M]) Find(ctx context.Context, key *K) (*M, error) {
	return findEphemeral[K, M](ctx, t.col, key)
}

// EphemeralTableReader reads an ephemeral table without taking part in its
// ownership: it writes no entries and runs no cleanup.
type EphemeralTableReader[K any, M any] struct {
	col db.StoreCollection
}

// Find returns the entry's meta value; NotFound if there is none.
func (r *EphemeralTableReader[K, M]) Find(ctx context.Context, key *K) (*M, error) {
	return findEphemeral[K, M](ctx, r.col, key)
}

func findEphemeral[K any, M any](ctx context.Context, col db.StoreCollection, key *K) (*M, error) {
	data := &ephemeralData[M]{}
	err := col.FindOne(ctx, key, data)
	if err != nil {
		return nil, err
	}
	if data.Meta == nil {
		// written with no meta
		return new(M), nil
	}
	return data.Meta, nil
}

// checkEphemeralKey refuses a key type the table's watch cannot decode: each
// change event's key is decoded as a document.
func checkEphemeralKey[K any]() error {
	var k K
	kt := reflect.TypeOf(k)
	if kt == nil {
		return errors.Wrap(errors.InvalidArgument, "type parameter K must be a concrete type, not an interface")
	}
	if kt.Kind() != reflect.Struct {
		return errors.Wrapf(errors.InvalidArgument, "type parameter K must be a struct, got %s", kt)
	}
	return nil
}

// OpenEphemeralTableReader opens the table for Find only. It needs no owner
// table, so a process that writes no entries can read them. K must be a
// struct type, as for LocateEphemeralTable, since no entry can be written
// under any other.
func OpenEphemeralTableReader[K any, M any](store db.Store, name string) (*EphemeralTableReader[K, M], error) {
	if err := checkEphemeralKey[K](); err != nil {
		return nil, err
	}
	return &EphemeralTableReader[K, M]{
		col: store.GetCollection(name),
	}, nil
}

// LocateEphemeralTable returns the ephemeral table of the given name, the
// same one for the same store and name. K must be a struct type: the table
// watches its collection and decodes each event's key as a document, so any
// other key type is refused with InvalidArgument. A name already used with
// other type parameters, or by a LockTable, is refused with AlreadyExists.
func LocateEphemeralTable[K any, M any](store db.Store, name string) (*EphemeralTable[K, M], error) {
	muLockTables.Lock()
	defer muLockTables.Unlock()

	intf, ok := lockTables[lockTableKey{store.Name(), name}]
	if ok {
		table, ok := intf.(*EphemeralTable[K, M])
		if !ok {
			return nil, errors.Wrapf(errors.AlreadyExists, "Table name %s, is already in use", name)
		}
		return table, nil
	}

	// ensure owner table is initialized before proceeding further
	if ownerTable == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "Mandatory! owner table infra not initialized")
	}

	if err := checkEphemeralKey[K](); err != nil {
		return nil, err
	}
	// set the key type for watch notification decoding
	var k K
	kt := reflect.TypeOf(k)

	ctx, cancelFn := context.WithCancel(ownerTable.ctx)

	col := store.GetCollection(name)
	table := &EphemeralTable[K, M]{
		colName:  name,
		col:      col,
		ctx:      ctx,
		cancelFn: cancelFn,
		owned: &ownedEntries{
			col:    col,
			ctx:    ctx,
			kind:   "ephemeral entries",
			prefix: "ephemeral-table",
		},
	}

	err := col.SetKeyType(reflect.PointerTo(kt))
	if err != nil {
		cancelFn()
		return nil, errors.Wrapf(errors.GetErrCode(err), "failed to set key type for ephemeral table: %s", err)
	}

	// delete an owner's entries when it leaves the owner-table
	err = table.owned.watchOwnerRelease(ctx)
	if err != nil {
		cancelFn()
		return nil, err
	}

	// check the owner of every entry written, for one written by an owner
	// that aged out around the write
	err = col.Watch(ctx, nil, table.callback)
	if err != nil {
		cancelFn()
		return nil, err
	}

	// entries of owners that were gone before this process started watching
	// get no change event, so scan for them once
	table.owned.cleanupOrphans()

	lockTables[lockTableKey{store.Name(), name}] = table
	return table, nil
}
