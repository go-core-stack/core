// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package settings

import (
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
)

// These tests run against the MongoDB the rest of the repository's tests
// use. Only Test_LocateSettingsTable touches the package's own database,
// and it only reads.

var (
	clientOnce sync.Once
	client     db.StoreClient
)

func testClient() db.StoreClient {
	clientOnce.Do(func() {
		c, err := db.NewMongoClient(&db.MongoConfig{Host: "localhost", Port: "27017", Username: "root", Password: "password"})
		if err != nil {
			log.Panicf("failed to connect to mongo DB Error: %s", err)
		}
		client = c
	})
	return client
}

// testCollection returns a fresh, empty collection for one test.
func testCollection(t *testing.T) db.StoreCollection {
	t.Helper()
	// a fixed name per test, emptied before and after, so runs do not
	// accumulate collections
	name := "settings-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	col := testClient().GetCollection("test", name)
	_, _ = col.DeleteMany(context.Background(), bson.D{})
	t.Cleanup(func() { _, _ = col.DeleteMany(context.Background(), bson.D{}) })
	return col
}

// newTestTable returns a settings table on a fresh collection, seeded with
// stored as the platform record unless it is nil. stored is a raw document
// so a test can leave fields and sections out, or add a section the
// package does not define.
func newTestTable(t *testing.T, stored bson.D) (*SettingsTable, db.StoreCollection) {
	t.Helper()
	col := testCollection(t)
	if stored != nil {
		seed(t, col, stored)
	}
	tbl, err := newSettingsTable(col)
	if err != nil {
		t.Fatal(err)
	}
	return tbl, col
}

func seed(t *testing.T, col db.StoreCollection, stored bson.D) {
	t.Helper()
	if err := col.InsertOne(context.Background(), &Key{ID: platformID}, stored); err != nil {
		t.Fatal(err)
	}
}

// storedRecord is the platform record as stored, with a section "other"
// that only these tests write, standing for a setting of another writer.
type storedRecord struct {
	Licensing bson.M `bson:"licensing,omitempty"`
	Other     bson.M `bson:"other,omitempty"`
}

// read reads the platform record, or nil if missing.
func read(t *testing.T, col db.StoreCollection) *storedRecord {
	t.Helper()
	rec := &storedRecord{}
	err := col.FindOne(context.Background(), &Key{ID: platformID}, rec)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// stored reads the platform record's licensing section as a raw document,
// or nil if the record or the section is missing.
func stored(t *testing.T, col db.StoreCollection) bson.M {
	t.Helper()
	if rec := read(t, col); rec != nil {
		return rec.Licensing
	}
	return nil
}

// licensing returns a record holding section as its licensing section.
func licensing(section bson.D) bson.D {
	return bson.D{{Key: "licensing", Value: section}}
}

func record(unlicensed bool, basis int64, refusal bool) bson.D {
	return licensing(bson.D{
		{Key: "unlicensed", Value: unlicensed},
		{Key: "basis", Value: basis},
		{Key: "basisRefusal", Value: refusal},
		{Key: "updatedAt", Value: int64(1)},
	})
}

// otherSection is the section of another writer, written by the tests
var otherSection = bson.D{{Key: "mode", Value: "configured"}}

// withOther adds the other section to rec.
func withOther(rec bson.D) bson.D {
	return append(append(bson.D{}, rec...), bson.E{Key: "other", Value: otherSection})
}

type write struct {
	value   bool
	basis   int64
	refusal bool
}

func Test_SetUnlicensedOrdering(t *testing.T) {
	cases := []struct {
		name    string
		stored  bson.D // nil: no record
		write   write
		applied bool
	}{
		// a later basis
		{"later basis sets", record(false, 10, false), write{true, 11, true}, true},
		{"later basis clears true", record(true, 10, true), write{false, 11, false}, true},
		{"later basis keeps false", record(false, 10, true), write{false, 11, false}, true},

		// an earlier basis
		{"earlier basis does not set", record(false, 10, false), write{true, 9, true}, false},
		{"earlier basis does not clear", record(true, 10, true), write{false, 9, false}, false},

		// an equal basis, basisRefusal changing
		{"equal basis refusal false to true sets", record(false, 10, false), write{true, 10, true}, true},
		{"equal basis refusal false to true clears", record(true, 10, false), write{false, 10, true}, true},
		{"equal basis refusal true to false does not set", record(false, 10, true), write{true, 10, false}, false},
		{"equal basis refusal true to false does not clear", record(true, 10, true), write{false, 10, false}, false},

		// an equal pair
		{"equal pair false to true", record(false, 10, false), write{true, 10, false}, true},
		{"equal pair false to true with refusal", record(false, 10, true), write{true, 10, true}, true},
		{"equal pair true to false", record(true, 10, false), write{false, 10, false}, false},
		{"equal pair true to false with refusal", record(true, 10, true), write{false, 10, true}, false},
		{"equal pair true to true", record(true, 10, true), write{true, 10, true}, false},
		{"equal pair false to false", record(false, 10, false), write{false, 10, false}, false},

		// a missing record, and missing fields reading as false
		{"missing record set", nil, write{true, 10, true}, true},
		{"missing record clear", nil, write{false, 10, false}, true},
		{"missing basisRefusal equal basis refusal true", licensing(bson.D{{Key: "unlicensed", Value: true}, {Key: "basis", Value: int64(10)}}), write{false, 10, true}, true},
		{"missing basisRefusal equal pair false to true", licensing(bson.D{{Key: "unlicensed", Value: false}, {Key: "basis", Value: int64(10)}}), write{true, 10, false}, true},
		{"missing basisRefusal equal pair true to false", licensing(bson.D{{Key: "unlicensed", Value: true}, {Key: "basis", Value: int64(10)}}), write{false, 10, false}, false},
		{"missing unlicensed equal pair false to true", licensing(bson.D{{Key: "basis", Value: int64(10)}, {Key: "basisRefusal", Value: false}}), write{true, 10, false}, true},

		// a record without a licensing section, created by another
		// section's writer, takes any licensing write
		{"missing section set", withOther(nil), write{true, 10, true}, true},
		{"missing section clear", withOther(nil), write{false, 10, false}, true},

		// a section edited to have no basis stays as it is
		{"missing basis", licensing(bson.D{{Key: "unlicensed", Value: true}}), write{false, 99, true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tbl, col := newTestTable(t, c.stored)
			before := stored(t, col)
			start := time.Now().Unix()

			applied, err := tbl.SetUnlicensed(context.Background(), c.write.value, c.write.basis, c.write.refusal)
			if err != nil {
				t.Fatalf("SetUnlicensed: %s", err)
			}
			if applied != c.applied {
				t.Fatalf("applied = %v, want %v", applied, c.applied)
			}

			after := stored(t, col)
			if !c.applied {
				if !sameDoc(before, after) {
					t.Fatalf("record changed on a write not applied: before %v, after %v", before, after)
				}
				return
			}
			want := bson.M{
				"unlicensed":   c.write.value,
				"basis":        c.write.basis,
				"basisRefusal": c.write.refusal,
			}
			updatedAt, _ := after["updatedAt"].(int64)
			if updatedAt < start {
				t.Errorf("updatedAt = %v, want at least %d", after["updatedAt"], start)
			}
			delete(after, "updatedAt")
			if !sameDoc(want, after) {
				t.Errorf("record = %v, want %v", after, want)
			}
		})
	}
}

func sameDoc(a, b bson.M) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// Test_SetUnlicensedStaleWriter checks that a writer that has seen only
// older inputs leaves the record unchanged, updatedAt included.
func Test_SetUnlicensedStaleWriter(t *testing.T) {
	tbl, col := newTestTable(t, nil)
	ctx := context.Background()
	if applied, err := tbl.SetUnlicensed(ctx, true, 20, true); err != nil || !applied {
		t.Fatalf("first write: applied %v, err %v", applied, err)
	}
	before := stored(t, col)

	applied, err := tbl.SetUnlicensed(ctx, false, 10, false)
	if err != nil {
		t.Fatalf("stale write: %s", err)
	}
	if applied {
		t.Fatal("stale write applied")
	}
	if after := stored(t, col); !sameDoc(before, after) {
		t.Fatalf("record changed by a stale write: before %v, after %v", before, after)
	}
}

// Test_SetUnlicensedRaceOnFirstInsert races writers on a missing record;
// whatever the interleaving, the record ends at the latest pair.
func Test_SetUnlicensedRaceOnFirstInsert(t *testing.T) {
	ctx := context.Background()
	tbl, col := newTestTable(t, nil)
	for round := 0; round < 20; round++ {
		_, _ = col.DeleteMany(ctx, bson.D{})
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := int64(1); i <= 8; i++ {
			wg.Add(1)
			go func(basis int64) {
				defer wg.Done()
				// the latest writer clears, so a stale true cannot pass for it
				_, err := tbl.SetUnlicensed(ctx, basis != 8, basis, false)
				errs <- err
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %s", round, err)
			}
		}
		got := stored(t, col)
		if got["basis"] != int64(8) || got["unlicensed"] != false {
			t.Fatalf("round %d: record = %v, want the latest pair (basis 8, unlicensed false)", round, got)
		}
	}
}

// Test_SetUnlicensedCreatedAfterMiss makes deterministic the ordering a
// race on the first insert can take: the record created by another writer
// between the miss and the insert. The later writer applies; the earlier
// one leaves the record as it is.
func Test_SetUnlicensedCreatedAfterMiss(t *testing.T) {
	ctx := context.Background()
	later := &PlatformSettings{Licensing: &Licensing{Unlicensed: false, Basis: 20, BasisRefusal: false, UpdatedAt: 2}}
	earlier := &PlatformSettings{Licensing: &Licensing{Unlicensed: true, Basis: 10, BasisRefusal: true, UpdatedAt: 1}}

	t.Run("later writer", func(t *testing.T) {
		tbl, col := newTestTable(t, nil)
		if applied, err := tbl.applyForward(ctx, later); err != nil || applied {
			t.Fatalf("on a missing record: applied %v, err %v, want a miss", applied, err)
		}
		seed(t, col, record(true, 10, true))
		if applied, err := tbl.insertOrApply(ctx, later); err != nil || !applied {
			t.Fatalf("applied %v, err %v, want applied", applied, err)
		}
		if got := stored(t, col); got["basis"] != int64(20) || got["unlicensed"] != false {
			t.Fatalf("record = %v, want the later pair", got)
		}
	})
	t.Run("earlier writer", func(t *testing.T) {
		tbl, col := newTestTable(t, nil)
		if applied, err := tbl.applyForward(ctx, earlier); err != nil || applied {
			t.Fatalf("on a missing record: applied %v, err %v, want a miss", applied, err)
		}
		seed(t, col, record(false, 20, false))
		before := stored(t, col)
		if applied, err := tbl.insertOrApply(ctx, earlier); err != nil || applied {
			t.Fatalf("applied %v, err %v, want not applied", applied, err)
		}
		if after := stored(t, col); !sameDoc(before, after) {
			t.Fatalf("record changed: before %v, after %v", before, after)
		}
	})
}

// failingInserts is a collection whose inserts fail as unavailable.
type failingInserts struct {
	db.StoreCollection
}

func (c *failingInserts) InsertOne(ctx context.Context, key any, data any) error {
	return errors.Wrap(errors.Unavailable, "insert failed")
}

func (c *failingInserts) Lifetime() context.Context {
	return c.StoreCollection.(db.ClientLifetime).Lifetime()
}

// Test_SetUnlicensedInsertFailure checks that an insert failing other than
// as a duplicate returns its error rather than a miss.
func Test_SetUnlicensedInsertFailure(t *testing.T) {
	col := &failingInserts{StoreCollection: testCollection(t)}
	tbl, err := newSettingsTable(col)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := tbl.SetUnlicensed(context.Background(), true, 10, true)
	if !errors.IsUnavailable(err) || applied {
		t.Fatalf("SetUnlicensed on a failed insert: applied %v, err %v, want the insert's Unavailable error", applied, err)
	}
	if got := read(t, col.StoreCollection); got != nil {
		t.Fatalf("record created by a failed insert: %v", got)
	}
}

// Test_SetUnlicensedKeepsOtherSections checks that a licensing write sets
// the licensing section alone, leaving another writer's section as it is,
// on each path a write can take.
func Test_SetUnlicensedKeepsOtherSections(t *testing.T) {
	ctx := context.Background()
	later := &PlatformSettings{Licensing: &Licensing{Unlicensed: true, Basis: 20, BasisRefusal: true, UpdatedAt: 2}}
	check := func(t *testing.T, col db.StoreCollection, wantBasis int64) {
		t.Helper()
		rec := read(t, col)
		if rec == nil || !sameDoc(rec.Other, bson.M{"mode": "configured"}) {
			t.Fatalf("other section = %v, want it unchanged", rec)
		}
		if rec.Licensing["basis"] != wantBasis {
			t.Fatalf("licensing section = %v, want basis %d", rec.Licensing, wantBasis)
		}
	}

	cases := []struct {
		name   string
		stored bson.D
	}{
		{"record without licensing", withOther(nil)},
		{"record with licensing", withOther(record(false, 10, false))},
	}
	for _, c := range cases {
		t.Run("update "+c.name, func(t *testing.T) {
			tbl, col := newTestTable(t, c.stored)
			if applied, err := tbl.SetUnlicensed(ctx, true, 20, true); err != nil || !applied {
				t.Fatalf("applied %v, err %v, want applied", applied, err)
			}
			check(t, col, 20)
		})
		t.Run("insert then apply, "+c.name, func(t *testing.T) {
			tbl, col := newTestTable(t, nil)
			if applied, err := tbl.applyForward(ctx, later); err != nil || applied {
				t.Fatalf("on a missing record: applied %v, err %v, want a miss", applied, err)
			}
			seed(t, col, c.stored)
			if applied, err := tbl.insertOrApply(ctx, later); err != nil || !applied {
				t.Fatalf("applied %v, err %v, want applied", applied, err)
			}
			check(t, col, 20)
		})
	}
	t.Run("not applied", func(t *testing.T) {
		tbl, col := newTestTable(t, withOther(record(false, 30, false)))
		if applied, err := tbl.SetUnlicensed(ctx, true, 20, true); err != nil || applied {
			t.Fatalf("applied %v, err %v, want not applied", applied, err)
		}
		check(t, col, 30)
	})
}

// Test_Unlicensed checks the cached read: a missing record reads false, and
// the cache follows applied writes.
func Test_Unlicensed(t *testing.T) {
	tbl, _ := newTestTable(t, nil)
	ctx := context.Background()
	if tbl.Unlicensed() {
		t.Fatal("missing record reads true")
	}
	if _, err := tbl.SetUnlicensed(ctx, true, 10, true); err != nil {
		t.Fatal(err)
	}
	waitUnlicensed(t, tbl, true)
	if _, err := tbl.SetUnlicensed(ctx, false, 11, false); err != nil {
		t.Fatal(err)
	}
	waitUnlicensed(t, tbl, false)
}

// Test_UnlicensedLoadedAtLocate checks that a record written before the
// table is initialized is read from the cache once it returns.
func Test_UnlicensedLoadedAtLocate(t *testing.T) {
	tbl, _ := newTestTable(t, record(true, 10, true))
	if !tbl.Unlicensed() {
		t.Fatal("record stored before initialization not in the cache")
	}
}

// Test_UnlicensedWithOtherSection checks the cached read of a record
// carrying another writer's section: without a licensing section it reads
// false, and with one it reads the section's value.
func Test_UnlicensedWithOtherSection(t *testing.T) {
	tbl, _ := newTestTable(t, withOther(nil))
	if tbl.Unlicensed() {
		t.Fatal("record without a licensing section reads true")
	}
	tbl, _ = newTestTable(t, withOther(record(true, 10, true)))
	if !tbl.Unlicensed() {
		t.Fatal("record with another section and a licensing section reads false")
	}
}

func waitUnlicensed(t *testing.T, tbl *SettingsTable, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for tbl.Unlicensed() != want {
		if time.Now().After(deadline) {
			t.Fatalf("Unlicensed() did not become %v", want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func Test_LocateSettingsTable(t *testing.T) {
	if _, err := GetSettingsTable(); !errors.IsNotFound(err) {
		t.Fatalf("GetSettingsTable before Locate: err %v, want NotFound", err)
	}
	first, err := LocateSettingsTable(testClient())
	if err != nil {
		t.Fatal(err)
	}
	second, err := LocateSettingsTable(testClient())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("second Locate returned a different table")
	}
	got, err := GetSettingsTable()
	if err != nil || got != first {
		t.Fatalf("GetSettingsTable after Locate: %p, %v, want %p", got, err, first)
	}
}
