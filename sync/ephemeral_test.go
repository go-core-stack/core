// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
)

type ephKey struct {
	Scope string
	Name  string
}

type ephMeta struct {
	Endpoint string
	Replicas int
}

const (
	// set in the child process of Test_EphemeralReaderWithoutOwner
	envReaderChild = "SYNC_EPHEMERAL_READER_CHILD"
	envReaderTable = "SYNC_EPHEMERAL_READER_TABLE"
)

func ephemeralTestStore(t *testing.T, withOwner bool) db.Store {
	t.Helper()
	config := &db.MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}
	client, err := db.NewMongoClient(config)
	if err != nil {
		t.Fatalf("failed to connect to mongo DB Error: %s", err)
	}
	s := client.GetDataStore("test-sync")
	if withOwner {
		err = InitializeOwner(context.Background(), s, "test-owner")
		if err != nil && !errors.IsAlreadyExists(err) {
			t.Fatalf("Got error while initializing sync owner %s", err)
		}
	}
	return s
}

// a table name of its own, so LocateEphemeralTable initialises it afresh
func ephemeralTableName() string {
	return "eph-test-" + uuid.NewString()
}

// fakeOwner adds an owner-table entry for a process that does not exist, so
// entries can be written as another owner.
func fakeOwner(t *testing.T) string {
	t.Helper()
	name := "other-owner-" + uuid.NewString()
	err := ownerTable.col.InsertOne(context.Background(), &ownerKey{Name: name}, &ownerData{LastSeen: time.Now().Unix()})
	if err != nil {
		t.Fatalf("failed to add owner %s: %s", name, err)
	}
	return name
}

func insertAs(t *testing.T, col db.StoreCollection, owner string, key *ephKey, meta *ephMeta) {
	t.Helper()
	data := &ephemeralData[ephMeta]{CreateTime: time.Now().Unix(), Owner: owner, Meta: meta}
	if err := col.InsertOne(context.Background(), key, data); err != nil {
		t.Fatalf("failed to insert entry as %s: %s", owner, err)
	}
}

// waitNotFound polls Find until the entry is gone; deletes by owner cleanup
// arrive through change streams, so they are not immediate.
func waitNotFound(t *testing.T, tbl *EphemeralTable[ephKey, ephMeta], key *ephKey) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := tbl.Find(context.Background(), key)
		if errors.IsNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("entry %v still present (last error: %v)", key, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func Test_EphemeralCreateFindClose(t *testing.T) {
	s := ephemeralTestStore(t, true)
	tbl, err := LocateEphemeralTable[ephKey, ephMeta](s, ephemeralTableName())
	if err != nil {
		t.Fatalf("failed to locate ephemeral table: %s", err)
	}

	key := &ephKey{Scope: "scope-1", Name: "conn"}
	entry, err := tbl.Create(context.Background(), key, &ephMeta{Endpoint: "10.0.0.1:9000", Replicas: 2})
	if err != nil {
		t.Fatalf("failed to create entry: %s", err)
	}

	meta, err := tbl.Find(context.Background(), key)
	if err != nil {
		t.Fatalf("failed to find entry: %s", err)
	}
	if meta.Endpoint != "10.0.0.1:9000" || meta.Replicas != 2 {
		t.Errorf("found meta %+v, want the one written", meta)
	}

	_, err = tbl.Create(context.Background(), key, &ephMeta{Endpoint: "other"})
	if !errors.IsAlreadyExists(err) {
		t.Errorf("duplicate create: got %v, want AlreadyExists", err)
	}

	if err := entry.Close(); err != nil {
		t.Fatalf("failed to close entry: %s", err)
	}
	_, err = tbl.Find(context.Background(), key)
	if !errors.IsNotFound(err) {
		t.Errorf("find after close: got %v, want NotFound", err)
	}

	// an entry written with no meta reads back as the zero value
	entry, err = tbl.Create(context.Background(), key, nil)
	if err != nil {
		t.Fatalf("failed to create entry without meta: %s", err)
	}
	defer func() { _ = entry.Close() }()
	meta, err = tbl.Find(context.Background(), key)
	if err != nil || meta == nil || *meta != (ephMeta{}) {
		t.Errorf("find of entry without meta: got %+v, %v; want zero meta", meta, err)
	}
}

func Test_EphemeralCloseLeavesRecreatedEntry(t *testing.T) {
	s := ephemeralTestStore(t, true)
	tbl, err := LocateEphemeralTable[ephKey, ephMeta](s, ephemeralTableName())
	if err != nil {
		t.Fatalf("failed to locate ephemeral table: %s", err)
	}

	key := &ephKey{Scope: "scope-1", Name: "conn"}
	entry, err := tbl.Create(context.Background(), key, &ephMeta{Endpoint: "mine"})
	if err != nil {
		t.Fatalf("failed to create entry: %s", err)
	}

	// owner cleanup removes this process's entry, and another owner then
	// creates one under the same key
	if err := tbl.col.DeleteOne(context.Background(), key); err != nil {
		t.Fatalf("failed to delete entry: %s", err)
	}
	other := fakeOwner(t)
	defer func() { _ = ownerTable.col.DeleteOne(context.Background(), &ownerKey{Name: other}) }()
	insertAs(t, tbl.col, other, key, &ephMeta{Endpoint: "theirs"})

	err = entry.Close()
	if !errors.IsNotFound(err) {
		t.Errorf("late close: got %v, want NotFound", err)
	}
	meta, err := tbl.Find(context.Background(), key)
	if err != nil {
		t.Fatalf("the other owner's entry is gone after a late close: %s", err)
	}
	if meta.Endpoint != "theirs" {
		t.Errorf("found meta %+v, want the other owner's", meta)
	}
}

func Test_EphemeralOwnerReleaseDeletesEntries(t *testing.T) {
	s := ephemeralTestStore(t, true)
	tbl, err := LocateEphemeralTable[ephKey, ephMeta](s, ephemeralTableName())
	if err != nil {
		t.Fatalf("failed to locate ephemeral table: %s", err)
	}

	other := fakeOwner(t)
	k1 := &ephKey{Scope: "scope-1", Name: "a"}
	k2 := &ephKey{Scope: "scope-1", Name: "b"}
	insertAs(t, tbl.col, other, k1, &ephMeta{Endpoint: "a"})
	insertAs(t, tbl.col, other, k2, &ephMeta{Endpoint: "b"})

	mine := &ephKey{Scope: "scope-1", Name: "mine"}
	entry, err := tbl.Create(context.Background(), mine, &ephMeta{Endpoint: "mine"})
	if err != nil {
		t.Fatalf("failed to create entry: %s", err)
	}
	defer func() { _ = entry.Close() }()

	if _, err := tbl.Find(context.Background(), k1); err != nil {
		t.Fatalf("the other owner's entry is missing before its release: %s", err)
	}

	if err := ownerTable.col.DeleteOne(context.Background(), &ownerKey{Name: other}); err != nil {
		t.Fatalf("failed to release owner: %s", err)
	}
	waitNotFound(t, tbl, k1)
	waitNotFound(t, tbl, k2)

	if _, err := tbl.Find(context.Background(), mine); err != nil {
		t.Errorf("this process's entry went with another owner's release: %s", err)
	}
}

func Test_EphemeralStartupScanDeletesOrphans(t *testing.T) {
	s := ephemeralTestStore(t, true)
	name := ephemeralTableName()

	// written before the table is located, by an owner that is already gone,
	// so no change event will ever report it
	gone := "gone-owner-" + uuid.NewString()
	key := &ephKey{Scope: "scope-1", Name: "orphan"}
	insertAs(t, s.GetCollection(name), gone, key, &ephMeta{Endpoint: "orphan"})

	tbl, err := LocateEphemeralTable[ephKey, ephMeta](s, name)
	if err != nil {
		t.Fatalf("failed to locate ephemeral table: %s", err)
	}
	_, err = tbl.Find(context.Background(), key)
	if !errors.IsNotFound(err) {
		t.Errorf("orphaned entry after locate: got %v, want NotFound", err)
	}
}

func Test_EphemeralRegistry(t *testing.T) {
	s := ephemeralTestStore(t, true)
	name := ephemeralTableName()

	tbl, err := LocateEphemeralTable[ephKey, ephMeta](s, name)
	if err != nil {
		t.Fatalf("failed to locate ephemeral table: %s", err)
	}
	again, err := LocateEphemeralTable[ephKey, ephMeta](s, name)
	if err != nil || again != tbl {
		t.Errorf("second locate: got %p, %v; want the same table %p", again, err, tbl)
	}

	if _, err := LocateEphemeralTable[ephKey, string](s, name); !errors.IsAlreadyExists(err) {
		t.Errorf("locate with other type parameters: got %v, want AlreadyExists", err)
	}
	if _, err := LocateLockTable[ephKey](s, name); !errors.IsAlreadyExists(err) {
		t.Errorf("lock table on an ephemeral table's name: got %v, want AlreadyExists", err)
	}

	lockName := ephemeralTableName()
	if _, err := LocateLockTable[ephKey](s, lockName); err != nil {
		t.Fatalf("failed to locate lock table: %s", err)
	}
	if _, err := LocateEphemeralTable[ephKey, ephMeta](s, lockName); !errors.IsAlreadyExists(err) {
		t.Errorf("ephemeral table on a lock table's name: got %v, want AlreadyExists", err)
	}

	if _, err := LocateEphemeralTable[any, ephMeta](s, ephemeralTableName()); !errors.IsInvalidArgument(err) {
		t.Errorf("interface key type: got %v, want InvalidArgument", err)
	}
	if _, err := LocateEphemeralTable[string, ephMeta](s, ephemeralTableName()); !errors.IsInvalidArgument(err) {
		t.Errorf("scalar key type: got %v, want InvalidArgument", err)
	}
	if _, err := OpenEphemeralTableReader[string, ephMeta](s, ephemeralTableName()); !errors.IsInvalidArgument(err) {
		t.Errorf("reader with scalar key type: got %v, want InvalidArgument", err)
	}
}

// Test_EphemeralReaderWithoutOwner reads from a child process that never
// initialises the owner infrastructure; ownerTable is a process-wide
// singleton, so this process cannot show it.
func Test_EphemeralReaderWithoutOwner(t *testing.T) {
	if os.Getenv(envReaderChild) == "1" {
		return
	}
	s := ephemeralTestStore(t, true)
	name := ephemeralTableName()
	tbl, err := LocateEphemeralTable[ephKey, ephMeta](s, name)
	if err != nil {
		t.Fatalf("failed to locate ephemeral table: %s", err)
	}
	entry, err := tbl.Create(context.Background(), &ephKey{Scope: "scope-1", Name: "present"}, &ephMeta{Endpoint: "present"})
	if err != nil {
		t.Fatalf("failed to create entry: %s", err)
	}
	defer func() { _ = entry.Close() }()

	cmd := exec.Command(os.Args[0], "-test.run=^Test_EphemeralReaderChild$", "-test.v")
	cmd.Env = append(os.Environ(), envReaderChild+"=1", envReaderTable+"="+name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reader child failed: %s\n%s", err, out)
	}
}

// Test_EphemeralReaderChild is the child of Test_EphemeralReaderWithoutOwner;
// it does nothing when run on its own.
func Test_EphemeralReaderChild(t *testing.T) {
	if os.Getenv(envReaderChild) != "1" {
		return
	}
	if ownerTable != nil {
		t.Fatalf("the child has owner infrastructure; the test proves nothing")
	}
	s := ephemeralTestStore(t, false)
	r, err := OpenEphemeralTableReader[ephKey, ephMeta](s, os.Getenv(envReaderTable))
	if err != nil {
		t.Fatalf("failed to open reader: %s", err)
	}
	meta, err := r.Find(context.Background(), &ephKey{Scope: "scope-1", Name: "present"})
	if err != nil || meta.Endpoint != "present" {
		t.Fatalf("reader find: got %+v, %v; want the entry", meta, err)
	}
	_, err = r.Find(context.Background(), &ephKey{Scope: "scope-1", Name: "missing"})
	if !errors.IsNotFound(err) {
		t.Fatalf("reader find of missing key: got %v, want NotFound", err)
	}
	if ownerTable != nil {
		t.Fatalf("opening a reader initialised owner infrastructure")
	}
	fmt.Println("reader child ok")
}
