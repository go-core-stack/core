// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

var (
	// map for holding initialized lock and ephemeral tables; one name
	// in one store is one table, whatever its kind and type parameters
	lockTables map[lockTableKey]interface{} = make(map[lockTableKey]interface{})

	// mutex for securing lockTable Map
	muLockTables sync.Mutex
)

type lockTableKey struct {
	DbName  string
	ColName string
}

type Lock interface {
	Close() error
}

type lockImpl[K any] struct {
	key *K
	tbl *LockTable[K]
}

func (l *lockImpl[K]) Close() error {
	return l.tbl.col.DeleteOne(context.Background(), l.key)
}

type lockData struct {
	CreateTime int64  `bson:"createTime,omitempty"`
	Owner      string `bson:"owner,omitempty"`
}

type lockKeyOnly[K any] struct {
	Key K `bson:"_id,omitempty"`
}

type LockTable[K any] struct {
	reconciler.ManagerImpl

	// collection name hosting locks for the table
	colName string

	// collection object for the database store
	col db.StoreCollection

	// context in which this lock table is being working on
	ctx context.Context

	// Context cancel function
	cancelFn context.CancelFunc

	// owner cleanup for the locks in col
	owned *ownedEntries
}

func (t *LockTable[K]) Callback(op string, wKey interface{}) {
	// on lock release (delete), notify registered controllers
	// allowing others to try acquiring the released lock
	if op == "delete" {
		t.NotifyCallback(wKey)
		return
	}

	// for non-delete events, verify the owner is still alive
	// and cleanup orphaned locks if the owner no longer exists
	t.owned.checkOwner(wKey)
}

// ReconcilerGetAllKeys returns list as reconciler for lock table
// should act only on lock release and shouldn't bother about locks
// currently held.
func (t *LockTable[K]) ReconcilerGetAllKeys() []any {
	return []any{}
}

// RegisterLockRelease allows a reconciler controller to subscribe for lock
// release notifications. The controller's Reconcile method will be called
// with the lock key whenever a lock is released.
func (t *LockTable[K]) RegisterLockRelease(name string, ctrl reconciler.Controller) error {
	return t.ManagerImpl.Register(name, ctrl)
}

func (t *LockTable[K]) TryAcquire(ctx context.Context, key *K) (Lock, error) {
	// if ownertable is not initialized, then lock infra cannot be used
	if ownerTable == nil || ownerTable.key == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "owner infra for lock is not initialized")
	}

	data := &lockData{
		CreateTime: time.Now().Unix(),
		Owner:      ownerTable.key.Name,
	}

	err := t.col.InsertOne(ctx, key, data)
	if err != nil {
		return nil, err
	}

	return &lockImpl[K]{
		key: key,
		tbl: t,
	}, nil
}

// LocateLockTable
func LocateLockTable[K any](store db.Store, name string) (*LockTable[K], error) {
	muLockTables.Lock()
	defer muLockTables.Unlock()

	var table *LockTable[K]
	intf, ok := lockTables[lockTableKey{store.Name(), name}]
	if !ok {
		// ensure owner table is initialized before proceeding further
		if ownerTable == nil {
			return nil, errors.Wrap(errors.InvalidArgument, "Mandatory! owner table infra not initialized")
		}

		ctx, cancelFn := context.WithCancel(ownerTable.ctx)

		// no existing table found, allocate a new one
		col := store.GetCollection(name)
		table = &LockTable[K]{
			colName:  name,
			col:      col,
			ctx:      ctx,
			cancelFn: cancelFn,
			owned: &ownedEntries{
				col:    col,
				ctx:    ctx,
				kind:   "locks",
				prefix: "lock-table",
			},
		}

		// set the key type for watch notification decoding
		var k K
		kt := reflect.TypeOf(k)
		if kt == nil {
			cancelFn()
			return nil, errors.Wrap(errors.InvalidArgument, "type parameter K must be a concrete type, not an interface")
		}
		err := col.SetKeyType(reflect.PointerTo(kt))
		if err != nil {
			cancelFn()
			return nil, errors.Wrapf(errors.GetErrCode(err), "failed to set key type for lock table: %s", err)
		}

		// initialize the reconciler manager for lock release notifications
		err = table.ManagerImpl.Initialize(ctx, table)
		if err != nil {
			cancelFn()
			return nil, err
		}

		// watch only for delete notification of lock owner
		err = table.owned.watchOwnerRelease(ctx)
		if err != nil {
			cancelFn()
			return nil, err
		}

		// register to watch for locks, this is relevant for external
		// notification and cleanup as part of handling of release of owners
		err = table.col.Watch(ctx, nil, table.Callback)
		if err != nil {
			cancelFn()
			return nil, err
		}

		// Scan for pre-existing orphaned locks whose owners no longer
		// exist. This handles the case where a previous replica died
		// and its owner entry was already aged out before we started
		// watching change streams — no change event will fire for
		// those stale locks, so we must clean them up eagerly.
		table.owned.cleanupOrphans()

		lockTables[lockTableKey{store.Name(), name}] = table
	} else {
		table, ok = intf.(*LockTable[K])
		if !ok {
			return nil, errors.Wrapf(errors.AlreadyExists, "Table name %s, is already in use", name)
		}
	}

	return table, nil
}
