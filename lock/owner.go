// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package lock

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
)

const (
	// collection name for Lock ownership table
	lockOwnerShipCollection = "lock-owner-table"

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
			Key:   "",
			Value: bson.D{{Key: "$lt", Value: filterTime}},
		},
	}
	_, err := t.col.DeleteMany(t.ctx, filter)
	if err != nil && !errors.IsNotFound(err) {
		log.Printf("failed to perform delete of aged lock owner entries")
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
	err := t.col.InsertOne(context.Background(), t.key, data)
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
					log.Printf("failed deleting self owner lock: %s, got error: %s", t.key.Name, err)
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

// Initialize the Lock Owner management constructs, anyone while working with
// this library requires to use this function before actually start consuming
// any functionality from here.
// Also it is callers responsibility to ensure providing uniform store
// definition for all the consuming processes to ensure synchronisation to work
// in a seemless manner
func InitializeLockOwner(ctx context.Context, store db.Store, name string) error {
	ownerTableInit.Lock()
	defer ownerTableInit.Unlock()
	if ownerTable != nil {
		return errors.Wrap(errors.AlreadyExists, "Lock Owner is already initialized")
	}

	col := store.GetCollection(lockOwnerShipCollection)

	ownerTable = &ownerTableType{
		ctx:            ctx,
		store:          store,
		col:            col,
		name:           name,
		updateInterval: time.Duration(defaultOwnerUpdateInterval * time.Second),
	}

	// allocate owner entry context
	return ownerTable.allocateOwner(name)
}
