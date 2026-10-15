// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	gosync "sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

// The owner cleanup of a lock table: a lock goes when its owner leaves the
// owner-table, when the table is located and its owner is already gone, and
// when a lock is written by an owner that is no longer present. Waiting
// controllers are told of each release.

func lockCleanupStore(t *testing.T) db.Store {
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
	err = InitializeOwner(context.Background(), s, "test-owner")
	if err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("Got error while initializing lock owner %s", err)
	}
	return s
}

// addLockOwner adds an owner-table entry for a process that does not exist,
// so locks can be written as another owner.
func addLockOwner(t *testing.T) string {
	t.Helper()
	name := "lock-owner-" + uuid.NewString()
	err := ownerTable.col.InsertOne(context.Background(), &ownerKey{Name: name}, &ownerData{LastSeen: time.Now().Unix()})
	if err != nil {
		t.Fatalf("failed to add owner %s: %s", name, err)
	}
	return name
}

func insertLockAs(t *testing.T, col db.StoreCollection, owner string, key *lockKey) {
	t.Helper()
	err := col.InsertOne(context.Background(), key, &lockData{CreateTime: time.Now().Unix(), Owner: owner})
	if err != nil {
		t.Fatalf("failed to write lock %v as %s: %s", key, owner, err)
	}
}

// waitLockGone polls until the lock record is gone; owner cleanup runs from
// change-stream callbacks.
func waitLockGone(t *testing.T, col db.StoreCollection, key *lockKey) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := col.FindOne(context.Background(), key, &lockData{})
		if errors.IsNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock %v still present: %v", key, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type releasedKeys struct {
	mu   gosync.Mutex
	keys map[lockKey]bool
}

func (c *releasedKeys) Reconcile(k any) (*reconciler.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key, ok := k.(*lockKey); ok {
		c.keys[*key] = true
	}
	return nil, nil
}

func (c *releasedKeys) waitFor(t *testing.T, key *lockKey) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		seen := c.keys[*key]
		c.mu.Unlock()
		if seen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no release notification for %v", key)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func Test_LockOwnerReleaseDeletesLocks(t *testing.T) {
	s := lockCleanupStore(t)
	name := "lock-cleanup-" + uuid.NewString()

	// written before the table is located, by an owner that is present: the
	// startup scan keeps them and no insert event reaches the table, so only
	// the owner-table delete watch can remove them
	other := addLockOwner(t)
	k1 := &lockKey{Scope: "scope-1", Name: "theirs-1"}
	k2 := &lockKey{Scope: "scope-1", Name: "theirs-2"}
	insertLockAs(t, s.GetCollection(name), other, k1)
	insertLockAs(t, s.GetCollection(name), other, k2)

	tbl, err := LocateLockTable[lockKey](s, name)
	if err != nil {
		t.Fatalf("failed to locate lock table: %s", err)
	}
	ctrl := &releasedKeys{keys: map[lockKey]bool{}}
	if err := tbl.RegisterLockRelease("test-owner-release", ctrl); err != nil {
		t.Fatalf("failed to register controller: %s", err)
	}
	if err := tbl.col.FindOne(context.Background(), k1, &lockData{}); err != nil {
		t.Fatalf("a present owner's lock went at locate: %s", err)
	}

	mine := &lockKey{Scope: "scope-1", Name: "mine"}
	lock, err := tbl.TryAcquire(context.Background(), mine)
	if err != nil {
		t.Fatalf("failed to acquire lock: %s", err)
	}
	defer func() { _ = lock.Close() }()

	if err := ownerTable.col.DeleteOne(context.Background(), &ownerKey{Name: other}); err != nil {
		t.Fatalf("failed to release owner: %s", err)
	}
	waitLockGone(t, tbl.col, k1)
	waitLockGone(t, tbl.col, k2)
	ctrl.waitFor(t, k1)
	ctrl.waitFor(t, k2)

	// the released keys can be acquired again, and this process's lock stayed
	again, err := tbl.TryAcquire(context.Background(), k1)
	if err != nil {
		t.Errorf("released lock could not be acquired: %s", err)
	} else {
		_ = again.Close()
	}
	if err := tbl.col.FindOne(context.Background(), mine, &lockData{}); err != nil {
		t.Errorf("this process's lock went with another owner's release: %s", err)
	}
}

func Test_LockStartupScanDeletesOrphans(t *testing.T) {
	s := lockCleanupStore(t)
	name := "lock-cleanup-" + uuid.NewString()

	// written before the table is located, by an owner that is already gone,
	// and by one that is still present
	col := s.GetCollection(name)
	orphan := &lockKey{Scope: "scope-1", Name: "orphan"}
	insertLockAs(t, col, "gone-owner-"+uuid.NewString(), orphan)
	live := addLockOwner(t)
	defer func() { _ = ownerTable.col.DeleteOne(context.Background(), &ownerKey{Name: live}) }()
	held := &lockKey{Scope: "scope-1", Name: "held"}
	insertLockAs(t, col, live, held)

	tbl, err := LocateLockTable[lockKey](s, name)
	if err != nil {
		t.Fatalf("failed to locate lock table: %s", err)
	}
	err = tbl.col.FindOne(context.Background(), orphan, &lockData{})
	if !errors.IsNotFound(err) {
		t.Errorf("orphaned lock after locate: got %v, want NotFound", err)
	}
	if err := tbl.col.FindOne(context.Background(), held, &lockData{}); err != nil {
		t.Errorf("a present owner's lock went at locate: %s", err)
	}
}

func Test_LockWrittenByAbsentOwnerIsDeleted(t *testing.T) {
	s := lockCleanupStore(t)
	tbl, err := LocateLockTable[lockKey](s, "lock-cleanup-"+uuid.NewString())
	if err != nil {
		t.Fatalf("failed to locate lock table: %s", err)
	}

	// an owner that aged out around its write: the insert event finds no
	// owner entry, and the lock is deleted
	key := &lockKey{Scope: "scope-1", Name: "late-write"}
	insertLockAs(t, tbl.col, "gone-owner-"+uuid.NewString(), key)
	waitLockGone(t, tbl.col, key)
}
