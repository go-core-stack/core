// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package settings

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/table"
)

const (
	// DatabaseName is the database holding the platform settings record
	DatabaseName = "settings"

	// CollectionName is the collection holding the platform settings record
	CollectionName = "platform_settings"

	// id of the one platform settings record
	platformID = "platform"
)

// Key is the key of a settings record. The platform settings record is
// the one with ID "platform".
type Key struct {
	ID string `bson:"id,omitempty"`
}

// PlatformSettings is the platform settings record: one section per
// setting, each owned by one writer and written whole by it. Each section
// is a pointer with omitempty, so a write of one section sets that section
// alone and never touches another. A missing record and a missing section
// both mean that the setting is not set.
type PlatformSettings struct {
	// licensing, written only by SetUnlicensed
	Licensing *Licensing `bson:"licensing,omitempty"`
}

// Licensing is the licensing section of the platform settings record.
//
// Every field is stored on every write, so none has omitempty and the
// section is always complete: with it, a zero value would be left out, and a
// section left without a basis matches no ordering case.
type Licensing struct {
	// true: restricted; false or missing: normal
	Unlicensed bool `bson:"unlicensed"`

	// the latest input time the value was decided from, in Unix seconds
	Basis int64 `bson:"basis"`

	// the input at Basis was a restricting one; missing reads as false
	BasisRefusal bool `bson:"basisRefusal"`

	// when the section was last written, in Unix seconds
	UpdatedAt int64 `bson:"updatedAt"`
}

// SettingsTable is the cached table of the platform settings record.
type SettingsTable struct {
	table.CachedTable[Key, PlatformSettings]
}

var (
	// singleton object for settings table
	settingsTable *SettingsTable

	// mutex for safe initialization of settings table
	settingsTableInit sync.Mutex
)

// GetSettingsTable returns the settings table located earlier by
// LocateSettingsTable, or a NotFound error if it has not been located.
func GetSettingsTable() (*SettingsTable, error) {
	settingsTableInit.Lock()
	defer settingsTableInit.Unlock()
	if settingsTable != nil {
		return settingsTable, nil
	}

	return nil, errors.Wrapf(errors.NotFound, "settings table not found")
}

// LocateSettingsTable locates the settings table in the package's own
// database, so every reader and the writer meet on the same record. It
// returns after the record, if any, is loaded into the cache. A second
// call returns the same table.
func LocateSettingsTable(client db.StoreClient) (*SettingsTable, error) {
	settingsTableInit.Lock()
	defer settingsTableInit.Unlock()
	if settingsTable != nil {
		return settingsTable, nil
	}

	tbl, err := newSettingsTable(client.GetCollection(DatabaseName, CollectionName))
	if err != nil {
		return nil, err
	}
	settingsTable = tbl

	return settingsTable, nil
}

// newSettingsTable initializes a settings table on col.
func newSettingsTable(col db.StoreCollection) (*SettingsTable, error) {
	tbl := &SettingsTable{}
	err := tbl.Initialize(col)
	if err != nil {
		return nil, err
	}
	return tbl, nil
}

// Unlicensed reports whether the platform is restricted, read from the
// cache. A missing record or licensing section reads as false.
func (t *SettingsTable) Unlicensed() bool {
	entry, err := t.Find(context.Background(), &Key{ID: platformID})
	if err != nil || entry.Licensing == nil {
		return false
	}
	return entry.Licensing.Unlicensed
}

// SetUnlicensed writes value, decided from inputs up to basis (Unix
// seconds), where basisRefusal says whether the input at basis was a
// restricting one. It writes the licensing section alone, whole, and
// applies only when it moves that section forward (see the package
// documentation); otherwise it returns applied = false with no error. A
// missing record or section is created. The write goes to the database;
// Unlicensed follows it once the change stream delivers it to the cache, so
// it can return the old value for a moment, in this process too.
//
// After an error the write may or may not have been applied. Calling again
// with the same arguments cannot move the record backward, but if the first
// call was applied the second returns applied = false: it finds a record
// that is not behind it, its own.
func (t *SettingsTable) SetUnlicensed(ctx context.Context, value bool, basis int64, basisRefusal bool) (bool, error) {
	entry := &PlatformSettings{
		Licensing: &Licensing{
			Unlicensed:   value,
			Basis:        basis,
			BasisRefusal: basisRefusal,
			UpdatedAt:    time.Now().Unix(),
		},
	}
	applied, err := t.applyForward(ctx, entry)
	if err != nil || applied {
		return applied, err
	}
	return t.insertOrApply(ctx, entry)
}

// applyForward writes entry's licensing section if it moves the existing
// record's section forward. A miss does not tell a missing record from one
// whose section is not behind entry's.
func (t *SettingsTable) applyForward(ctx context.Context, entry *PlatformSettings) (bool, error) {
	return t.UpdateWithOpts(ctx, &Key{ID: platformID}, entry, table.If(table.Raw(forwardOrder(entry))))
}

// insertOrApply handles a miss of applyForward, which does not tell a
// missing record from one whose section is not behind entry's: it creates
// the record with entry, and if the record exists, created before the miss
// or by another writer since, it applies entry once more and returns that
// result.
// A record deleted between the insert and that apply stays missing, which
// reads as normal, and the write returns applied = false.
func (t *SettingsTable) insertOrApply(ctx context.Context, entry *PlatformSettings) (bool, error) {
	err := t.Insert(ctx, &Key{ID: platformID}, entry)
	if err == nil {
		return true, nil
	}
	if !errors.IsAlreadyExists(err) {
		return false, err
	}
	return t.applyForward(ctx, entry)
}

// forwardOrder is the condition a stored record holds when entry's
// licensing section moves the stored one forward: the record has no
// licensing section; or entry's (basis, basisRefusal) is later than the
// stored pair, compared as time, then false before true; or the pairs are
// equal, the stored value is not true and entry's is true.
//
// A record without the section is matched explicitly, since a comparison
// on a missing licensing.basis matches nothing: a record created by
// another section's writer would otherwise never take a licensing write.
// $ne: true is used for false, because a missing field does not match
// false and missing reads as false.
func forwardOrder(entry *PlatformSettings) bson.D {
	l := entry.Licensing
	notTrue := bson.D{{Key: "$ne", Value: true}}
	cases := bson.A{
		bson.D{{Key: "licensing", Value: bson.D{{Key: "$exists", Value: false}}}},
		bson.D{{Key: "licensing.basis", Value: bson.D{{Key: "$lt", Value: l.Basis}}}},
	}
	if l.BasisRefusal {
		cases = append(cases, bson.D{
			{Key: "licensing.basis", Value: l.Basis},
			{Key: "licensing.basisRefusal", Value: notTrue},
		})
	}
	if l.Unlicensed {
		var refusal any = notTrue
		if l.BasisRefusal {
			refusal = true
		}
		cases = append(cases, bson.D{
			{Key: "licensing.basis", Value: l.Basis},
			{Key: "licensing.basisRefusal", Value: refusal},
			{Key: "licensing.unlicensed", Value: notTrue},
		})
	}
	return bson.D{{Key: "$or", Value: cases}}
}
