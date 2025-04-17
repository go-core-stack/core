package sync

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

var (
	// map for holding initialized lock tables
	lockTables map[string]*LockTable = make(map[string]*LockTable)

	// mutex for securing lockTable Map
	muLockTables sync.Mutex
)

type Lock interface {
	Close() error
}

type lockImpl struct {
	key interface{}
	tbl *LockTable
}

func (l *lockImpl) Close() error {
	return l.tbl.col.DeleteOne(context.Background(), l.key)
}

type lockData struct {
	CreateTime int64  `bson:"createTime,omitempty"`
	Owner      string `bson:"owner,omitempty"`
}

type LockTable struct {
	// collection name hosting locks for the table
	colName string

	// collection object for the database store
	col db.StoreCollection

	// context in which this lock table is being working on
	ctx context.Context

	// Context cancel function
	cancelFn context.CancelFunc
}

func (t *LockTable) Callback(op string, wKey interface{}) {
	// handle callback as and when needed
	// we may need notification of release of locks
	// allowing others to start working on it
}

func (t *LockTable) handleOwnerRelease(op string, wKey interface{}) {
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

func (t *LockTable) TryAcquire(ctx context.Context, key interface{}) (Lock, error) {
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

	return &lockImpl{
		key: key,
		tbl: t,
	}, nil
}

func LocateLockTable(store db.Store, name string) (*LockTable, error) {
	muLockTables.Lock()
	defer muLockTables.Unlock()

	table, ok := lockTables[name]
	if !ok {
		// ensure owner table is initialized before proceeding further
		if ownerTable == nil {
			return nil, errors.Wrap(errors.InvalidArgument, "Mandatory! owner table infra not initialized")
		}

		ctx, cancelFn := context.WithCancel(ownerTable.ctx)

		// no existing table found, allocate a new one
		col := store.GetCollection(name)
		table = &LockTable{
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

		lockTables[name] = table
	}

	return table, nil
}
