// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"log"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
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

type LockTable[K any] struct {
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
	// handle callback as and when needed
	// we may need notification of release of locks
	// allowing others to start working on it
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
		err := ownerTable.col.Watch(ctx, matchDeleteStage, table.handleOwnerRelease)
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

		lockTables[lockTableKey{store.Name(), name}] = table
	} else {
		table, ok = intf.(*LockTable[K])
		if !ok {
			return nil, errors.Wrapf(errors.AlreadyExists, "Table name %s, is already in use", name)
		}
	}

	return table, nil
}
