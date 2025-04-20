// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
)

const (
	// collection name for sync ownership table
	ownerShipCollection = "owner-table"

	// default periodic interval for updating
	// last seen time for owner, in seconds
	defaultOwnerUpdateInterval = 10

	// default number of iterations missed before
	// aging out an entry
	defaultOwnerAgeUpdateMissed = 3
)

type ownerKey struct {
	Name string `bson:"name,omitempty"`
}

type ownerData struct {
	LastSeen int64 `bson:"lastSeen,omitempty"`
}

type ownerTableType struct {
	ctx            context.Context
	store          db.Store
	col            db.StoreCollection
	name           string
	key            *ownerKey
	updateInterval time.Duration
}

func (t *ownerTableType) DeleteCallback(op string, wKey interface{}) {
	key := wKey.(*ownerKey)
	if key.Name == t.key.Name {
		log.Panicln("OnwerTable: receiving delete notification of self")
	}
}

func (t *ownerTableType) updateLastSeen() {
	data := &ownerData{
		LastSeen: time.Now().Unix(),
	}
	err := t.col.UpdateOne(context.Background(), t.key, data, false)
	if err != nil {
		log.Panicf("failed to update ownership table: %s", err)
	}
}

func (t *ownerTableType) deleteAgedOwnerTableEntries() {
	// delete multiple entires, those have atleast missed
	// threshold count of age to timout an entry
	filterTime := time.Now().Add(-1 * defaultOwnerAgeUpdateMissed * t.updateInterval).Unix()

	filter := bson.D{
		{
			Key:   "lastSeen",
			Value: bson.D{{Key: "$lt", Value: filterTime}},
		},
	}
	_, err := t.col.DeleteMany(t.ctx, filter)
	if err != nil && !errors.IsNotFound(err) {
		log.Printf("failed to perform delete of aged owner table entries")
	}
}

func (t *ownerTableType) allocateOwner(name string) error {
	id := name
	if id == "" {
		id = "unknown"
	}
	data := &ownerData{
		LastSeen: time.Now().Unix(),
	}
	uid := uuid.New()
	if t.key == nil {
		t.key = &ownerKey{
			Name: id + "-" + uid.String(),
		}
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

	err := t.col.SetKeyType(reflect.TypeOf(&ownerKey{}))
	if err != nil {
		return errors.Wrapf(errors.GetErrCode(err), "Got error while setting key type for watch notification: %s", err)
	}

	// watch only for delete notification
	err = t.col.Watch(t.ctx, matchDeleteStage, t.DeleteCallback)
	if err != nil {
		return err
	}

	err = t.col.InsertOne(context.Background(), t.key, data)
	if err != nil {
		return err
	}

	// start a go routine to keep updating the Last Seen time
	// periodically, ensuring that we keep the entry active and
	// not letting it age out
	go func() {
		ticker := time.NewTicker(t.updateInterval)
		for {
			select {
			case <-ticker.C:
				// Trigger a delete for all entries those have atleast
				// missed default number of updates to the database
				// this helps aging out the entry
				t.updateLastSeen()
				t.deleteAgedOwnerTableEntries()
			case <-t.ctx.Done():
				// exit the update loop as the context under which
				// this was running is already closed
				// while exiting also ensure that self ownership is
				// released
				err = t.col.DeleteOne(context.Background(), t.key)
				if err != nil {
					log.Printf("failed deleting self owner entry: %s, got error: %s", t.key.Name, err)
				}
				return
			}
		}
	}()
	return nil
}

var (
	// singleton object for owner table
	ownerTable *ownerTableType

	// mutex for safe initialization of owner table
	ownerTableInit sync.Mutex
)

// Initialize the Sync Owner management constructs, anyone while working with
// this library requires to use this function before actually start consuming
// any functionality from here.
// Also it is callers responsibility to ensure providing uniform store
// definition for all the consuming processes to ensure synchronisation to work
// in a seemless manner
func InitializeOwner(ctx context.Context, store db.Store, name string) error {
	return InitializeOwnerWithUpdateInterval(ctx, store, name, defaultOwnerUpdateInterval)
}

// Initialize the Owner management constructs, anyone while working with
// this library requires to use this function before actually start consuming
// any functionality from here.
// This also allows specifying the interval to ensuring configurability
// Also it is callers responsibility to ensure providing uniform store
// definition for all the consuming processes to ensure synchronisation to work
// in a seemless manner
func InitializeOwnerWithUpdateInterval(ctx context.Context, store db.Store, name string, interval time.Duration) error {
	ownerTableInit.Lock()
	defer ownerTableInit.Unlock()
	if ownerTable != nil {
		return errors.Wrap(errors.AlreadyExists, "Sync Owner Table is already initialized")
	}

	col := store.GetCollection(ownerShipCollection)

	ownerTable = &ownerTableType{
		ctx:            ctx,
		store:          store,
		col:            col,
		name:           name,
		updateInterval: time.Duration(interval * time.Second),
	}

	// allocate owner entry context
	return ownerTable.allocateOwner(name)
}
