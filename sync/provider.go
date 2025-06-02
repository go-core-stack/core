// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"log"
	"reflect"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

const (
	// standard provider table name
	defaultProviderTableName = "provider-table"
)

var (
	// singleton object for provider table
	providerTable *ProviderTable
)

type providerKey struct {
	ExtKey     any       `bson:"extKey,omitempty"`
	ProviderId uuid.UUID `bson:"providerId,omitempty"`
	CreateTime int64     `bson:"createTime,omitempty"`
}

type Provider struct {
	key *providerKey
	tbl *ProviderTable
}

type listKeyEntry struct {
	Key *providerKey `bson:"_id,omitempty"`
}

func (p *Provider) Close() error {
	return p.tbl.col.DeleteOne(context.Background(), p.key)
}

type providerData struct {
	Owner string `bson:"owner,omitempty"`
}

type ProviderTable struct {
	// collection name hosting locks for the table
	colName string

	// collection object for the database store
	col db.StoreCollection

	// context in which this lock table is being working on
	ctx context.Context

	// Context cancel function
	cancelFn context.CancelFunc

	// observer table
	oTbl *observerTable
}

// Provider Table callback function, currently meant for
// clearing providers once the owner corresponding to those
// providers no longer exists
// eventually we will handle observers also as part of this
func (t *ProviderTable) Callback(op string, wKey interface{}) {
	key := wKey.(*providerKey)

	// ensure updating the observer table based on availability
	// or unavailability of provider
	obKey := &observerCountKey{
		ExtKey: key.ExtKey,
	}
	cnt, err := t.col.Count(context.Background(), obKey)
	if err != nil {
		log.Panicf("failed to fetch count of providers: %s", err)
	}
	if cnt == 0 {
		t.oTbl.deleteProvider(obKey.ExtKey)
	} else {
		t.oTbl.insertProvider(obKey.ExtKey)
	}

	entry := &providerData{}
	err = t.col.FindOne(context.Background(), key, entry)
	if err != nil {
		if errors.IsNotFound(err) {
			return
		}
		log.Panicf("failed to find the entry while working with: %s", err)
	}

	oKey := &ownerKey{
		Name: entry.Owner,
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
				log.Panicf("failed to perform delete of providers for owner %s, got error: %s", oKey.Name, err)
			}
		}
	}
}

// callback function to handle release of owner object
// responsible for clearing all the providers initiated
// by the specific owner, typically this is run by other
// participating microservices / processes
func (t *ProviderTable) handleOwnerRelease(op string, wKey any) {
	key := wKey.(*ownerKey)

	filter := bson.D{{
		Key:   "owner",
		Value: key.Name,
	}}
	_, err := t.col.DeleteMany(t.ctx, filter)
	if err != nil && !errors.IsNotFound(err) {
		log.Panicf("failed to perform delete of providers for owner %s, got error: %s", key.Name, err)
	}
}

// Allow a reconciler controller to register and get notified for availability
// and unavailability of providers
func (t *ProviderTable) Register(name string, crtl reconciler.Controller) error {
	return t.oTbl.Register(name, crtl)
}

// Get List of Providers
func (t *ProviderTable) GetProviderList() []any {
	return t.oTbl.getProviderList()
}

// Checks if provider exists
func (t *ProviderTable) IsProviderAvailable(key any) bool {
	return t.oTbl.isProviderAvailable(key)
}

// create provider based on the specified key, typically a string,
// Returns Provider handle, allowing to close the provider
func (t *ProviderTable) CreateProvider(ctx context.Context, extKey any) (*Provider, error) {
	// if ownertable is not initialized, then lock infra cannot be used
	if ownerTable == nil || ownerTable.key == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "owner infra for provider table is not initialized")
	}

	key := &providerKey{
		ExtKey:     extKey,
		CreateTime: time.Now().Unix(),
		ProviderId: uuid.New(),
	}

	data := &providerData{
		Owner: ownerTable.key.Name,
	}

	err := t.col.InsertOne(ctx, key, data)
	if err != nil {
		return nil, err
	}

	return &Provider{
		key: key,
		tbl: t,
	}, nil
}

// Locate Provider table with pre-specified table name
// while working out of standard provider table
func LocateProviderTable(store db.Store) (*ProviderTable, error) {
	return LocateProviderTableWithName(store, defaultProviderTableName)
}

// Locate Provider table with specific table name
// meant for consumers want to work out of non standard Provider tables
func LocateProviderTableWithName(store db.Store, name string) (*ProviderTable, error) {
	if providerTable != nil {
		return providerTable, nil
	}

	// ensure owner table is initialized before proceeding further
	if ownerTable == nil {
		return nil, errors.Wrap(errors.InvalidArgument, "Mandatory! owner table infra not initialized")
	}

	ctx, cancelFn := context.WithCancel(ownerTable.ctx)

	// no existing table found, allocate a new one
	col := store.GetCollection(name)
	table := &ProviderTable{
		colName:  name,
		col:      col,
		ctx:      ctx,
		cancelFn: cancelFn,
		oTbl: &observerTable{
			providers: make(map[any]struct{}),
		},
	}

	err := table.oTbl.Initialize(ctx, table.oTbl)
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

	err = table.col.SetKeyType(reflect.TypeOf(&providerKey{}))
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

	go func() {
		list := []listKeyEntry{}

		err = table.col.FindMany(ctx, nil, &list)
		if err != nil {
			return
		}

		for _, entry := range list {
			table.Callback("insert", entry.Key)
		}
	}()

	return table, nil
}
