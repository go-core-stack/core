// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"log"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
)

// ownedEntry is the part of a stored entry that owner cleanup reads: every
// table whose entries belong to the process that wrote them stores the
// owner's name under "owner".
type ownedEntry struct {
	Owner string `bson:"owner,omitempty"`
}

// ownedEntries is the owner cleanup shared by every table whose entries live
// only as long as the process that wrote them: when an owner leaves the
// owner-table, or is found absent, every entry it wrote is deleted.
type ownedEntries struct {
	// collection holding the entries
	col db.StoreCollection

	// context the table works in; ends with the owner infrastructure
	ctx context.Context

	// what the entries are, for log messages ("locks")
	kind string

	// log prefix naming the table kind ("lock-table")
	prefix string
}

// deleteOwner deletes every entry written by the named owner. Finding none
// is not an error.
func (o *ownedEntries) deleteOwner(owner string) error {
	filter := bson.D{{
		Key:   "owner",
		Value: owner,
	}}
	_, err := o.col.DeleteMany(o.ctx, filter)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// releaseOwned deletes one entry only while it is still the named owner's:
// the delete matches the key and the owner, so a release arriving after
// owner cleanup has removed the entry cannot delete one another process has
// since written under the same key. It returns NotFound when there is
// nothing of the owner's left to delete. Every handle that releases an
// owned entry (a lock, an ephemeral entry) goes through it.
func releaseOwned(col db.StoreCollection, key any, owner string) error {
	filter := bson.D{
		{Key: "_id", Value: key},
		{Key: "owner", Value: owner},
	}
	_, err := col.DeleteMany(context.Background(), filter)
	return err
}

// handleOwnerRelease is the owner-table delete watch: the owner has gone, so
// its entries go too.
func (o *ownedEntries) handleOwnerRelease(op string, wKey any) {
	key := wKey.(*ownerKey)
	if err := o.deleteOwner(key.Name); err != nil {
		log.Panicf("failed to perform delete of %s for owner %s, got error: %s", o.kind, key.Name, err)
	}
}

// checkOwner handles a non-delete event on an entry: if the entry's owner is
// no longer in the owner-table, every entry of that owner is deleted. This
// catches an entry written by an owner that aged out around the write.
func (o *ownedEntries) checkOwner(wKey any) {
	data := &ownedEntry{}
	err := o.col.FindOne(context.Background(), wKey, data)
	if err != nil {
		if errors.IsNotFound(err) {
			return
		}
		log.Panicln(o.prefix+": failed to find entry corresponding to key", wKey)
	}

	oKey := &ownerKey{
		Name: data.Owner,
	}
	oData := &ownerData{}
	err = ownerTable.col.FindOne(context.Background(), oKey, oData)
	if err != nil && errors.IsNotFound(err) {
		if err := o.deleteOwner(oKey.Name); err != nil {
			log.Panicf("failed to perform delete of %s for owner %s, got error: %s", o.kind, oKey.Name, err)
		}
	}
}

// cleanupOrphans scans all existing entries and deletes those whose owner no
// longer exists in the owner-table. It runs once when a table is located, for
// entries left by a process that died and aged out before this one started
// watching: no change event will fire for those.
func (o *ownedEntries) cleanupOrphans() {
	// only the owner is needed; an entry may carry meta of any size
	var entries []ownedEntry
	ownerOnly := options.Find().SetProjection(bson.D{{Key: "owner", Value: 1}})
	err := o.col.FindMany(context.Background(), nil, &entries, ownerOnly)
	if err != nil {
		log.Printf("%s: failed to scan %s for orphan cleanup: %v", o.prefix, o.kind, err)
		return
	}

	// Collect unique owners from all entries.
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
			if delErr := o.deleteOwner(ownerName); delErr != nil {
				log.Printf("%s: failed to delete orphaned %s for owner %s: %v", o.prefix, o.kind, ownerName, delErr)
			} else {
				log.Printf("%s: cleaned up orphaned %s for owner %s", o.prefix, o.kind, ownerName)
			}
		}
	}
}

// watchOwnerRelease subscribes to owner-table deletes, so an owner's entries
// are deleted when it goes.
func (o *ownedEntries) watchOwnerRelease(ctx context.Context) error {
	matchDeleteStage := mongo.Pipeline{
		bson.D{{
			Key: "$match",
			Value: bson.D{{
				Key:   "operationType",
				Value: "delete",
			}},
		}},
	}
	return ownerTable.col.Watch(ctx, matchDeleteStage, o.handleOwnerRelease)
}
