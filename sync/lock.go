// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"log"
	"reflect"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

var (
	// map for holding initialized lock tables
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
	data := &lockData{}
	err := t.col.FindOne(context.Background(), wKey, data)
	if err != nil {
		if errors.IsNotFound(err) {
			return
		}
		log.Panicln("failed to find lock entry corresponding to key", wKey)
	}

	oKey := &ownerKey{
		Name: data.Owner,
	}
	oData := &ownerData{}
	err = ownerTable.col.FindOne(context.Background(), oKey, oData)
	if err != nil {
		if errors.IsNotFound(err) {
			filter := bson.D{{
				Key:   "owner",
				Value: oKey.Name,
			}}
			_, err := t.col.DeleteMany(t.ctx, filter)
			if err != nil && !errors.IsNotFound(err) {
				log.Panicf("failed to perform delete of locks for owner %s, got error: %s", oKey.Name, err)
			}
		}
	}
}

// ReconcilerGetAllKeys returns all currently held lock keys.
// Used by the reconciler to enumerate all active locks.
func (t *LockTable[K]) ReconcilerGetAllKeys() []any {
	list := []lockKeyOnly[K]{}
	keys := []any{}
	err := t.col.FindMany(context.Background(), nil, &list)
	if err != nil {
		log.Panicf("got error while fetching all lock keys %s", err)
	}
	for _, k := range list {
		keys = append(keys, &k.Key)
	}
	return keys
}

// RegisterLockRelease allows a reconciler controller to subscribe for lock
// release notifications. The controller's Reconcile method will be called
// with the lock key whenever a lock is released.
func (t *LockTable[K]) RegisterLockRelease(name string, ctrl reconciler.Controller) error {
	return t.ManagerImpl.Register(name, ctrl)
}

func (t *LockTable[K]) handleOwnerRelease(op string, wKey interface{}) {
	key := wKey.(*ownerKey)

	filter := bson.D{{
		Key:   "owner",
		Value: key.Name,
	}}
	_, err := t.col.DeleteMany(t.ctx, filter)
	if err != nil && !errors.IsNotFound(err) {
		log.Panicf("failed to perform delete of Locks for owner %s, got error: %s", key.Name, err)
	}
}

// cleanupOrphanedLocks scans all existing locks and deletes any whose
// owner no longer exists in the owner-table. This is called once during
// LocateLockTable initialization to handle stale locks left by replicas
// that crashed before cleanup could run.
func (t *LockTable[K]) cleanupOrphanedLocks() {
	var entries []lockData
	err := t.col.FindMany(context.Background(), nil, &entries)
	if err != nil {
		log.Printf("lock-table: failed to scan locks for orphan cleanup: %v", err)
		return
	}

	// Collect unique owners from all locks.
	owners := make(map[string]bool)
	for _, e := range entries {
		if e.Owner != "" {
			owners[e.Owner] = true
		}
	}

	// Check each owner against the owner-table and delete orphans.
	for ownerName := range owners {
		oKey := &ownerKey{Name: ownerName}
		oData := &ownerData{}
		err := ownerTable.col.FindOne(context.Background(), oKey, oData)
		if err != nil && errors.IsNotFound(err) {
			filter := bson.D{{
				Key:   "owner",
				Value: ownerName,
			}}
			_, delErr := t.col.DeleteMany(t.ctx, filter)
			if delErr != nil && !errors.IsNotFound(delErr) {
				log.Printf("lock-table: failed to delete orphaned locks for owner %s: %v", ownerName, delErr)
			} else {
				log.Printf("lock-table: cleaned up orphaned locks for owner %s", ownerName)
			}
		}
	}
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

		matchDeleteStage := mongo.Pipeline{
			bson.D{{
				Key: "$match",
				Value: bson.D{{
					Key:   "operationType",
					Value: "delete",
				}},
			}},
		}

		// watch only for delete notification of lock owner
		err = ownerTable.col.Watch(ctx, matchDeleteStage, table.handleOwnerRelease)
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
		table.cleanupOrphanedLocks()

		lockTables[lockTableKey{store.Name(), name}] = table
	} else {
		table, ok = intf.(*LockTable[K])
		if !ok {
			return nil, errors.Wrapf(errors.AlreadyExists, "Table name %s, is already in use", name)
		}
	}

	return table, nil
}
