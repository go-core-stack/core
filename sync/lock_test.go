// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
	"github.com/google/uuid"
)

type lockKey struct {
	Scope string
	Name  string
}

func Test_LockBaseTesting(t *testing.T) {
	config := &db.MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}

	client, err := db.NewMongoClient(config)

	if err != nil {
		t.Errorf("failed to connect to mongo DB Error: %s", err)
		return
	}

	s := client.GetDataStore("test-sync")

	err = InitializeOwner(context.Background(), s, "test-owner")
	if err != nil && !errors.IsAlreadyExists(err) {
		t.Errorf("Got error while initializing lock owner %s", err)
	}

	tbl, err := LocateLockTable[lockKey](s, "demo-test")

	if err != nil {
		t.Errorf("failed to locate Lock Table: %s", err)
	}

	key1 := &lockKey{
		Scope: "scope-1",
		Name:  "test-key",
	}

	lock, err := tbl.TryAcquire(context.Background(), key1)
	if err != nil {
		t.Errorf("failed to acquire lock: %s", err)
	}
	_, err = tbl.TryAcquire(context.Background(), key1)
	if err == nil {
		t.Errorf("Acquired lock for %s:%s, which should have failed", key1.Scope, key1.Name)
	}
	key2 := &lockKey{
		Scope: "scope-2",
		Name:  "test-key",
	}

	lock1, err := tbl.TryAcquire(context.Background(), key2)
	if err != nil {
		t.Errorf("failed to acquire lock: %s", err)
	}
	if lock1 != nil {
		_ = lock1.Close()
	}
	if lock != nil {
		_ = lock.Close()
	}
}

func Test_LockDuplicatedTable(t *testing.T) {
	config := &db.MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}

	client, err := db.NewMongoClient(config)

	if err != nil {
		t.Errorf("failed to connect to mongo DB Error: %s", err)
		return
	}

	s := client.GetDataStore("test-sync")

	err = InitializeOwner(context.Background(), s, "test-owner")
	if err != nil && !errors.IsAlreadyExists(err) {
		t.Errorf("Got error while initializing lock owner %s", err)
	}

	_, err = LocateLockTable[interface{}](s, "demo-test")

	if err == nil || !errors.IsAlreadyExists(err) {
		t.Errorf("Should have received an error while locating Lock Table")
	}

	// verify that interface type is rejected even for a new table name
	_, err = LocateLockTable[interface{}](s, "demo-test-new")
	if err == nil || !errors.IsInvalidArgument(err) {
		t.Errorf("Should have received InvalidArgument error for interface type parameter, got: %v", err)
	}
}

type lockReleaseController struct {
	notified atomic.Int32
}

func (c *lockReleaseController) Reconcile(k any) (*reconciler.Result, error) {
	c.notified.Add(1)
	return nil, nil
}

func Test_LockReleaseNotification(t *testing.T) {
	config := &db.MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}

	client, err := db.NewMongoClient(config)
	if err != nil {
		t.Errorf("failed to connect to mongo DB Error: %s", err)
		return
	}

	s := client.GetDataStore("test-sync")

	err = InitializeOwner(context.Background(), s, "test-owner")
	if err != nil && !errors.IsAlreadyExists(err) {
		t.Errorf("Got error while initializing lock owner %s", err)
	}

	tbl, err := LocateLockTable[lockKey](s, "demo-notify-test")
	if err != nil {
		t.Errorf("failed to locate Lock Table: %s", err)
		return
	}

	ctrl := &lockReleaseController{}
	err = tbl.RegisterLockRelease("test-lock-release", ctrl)
	if err != nil {
		t.Errorf("failed to register controller: %s", err)
		return
	}

	key := &lockKey{
		Scope: "scope-notify",
		Name:  "test-key",
	}

	lock, err := tbl.TryAcquire(context.Background(), key)
	if err != nil {
		t.Errorf("failed to acquire lock: %s", err)
		return
	}

	// release the lock, should trigger notification
	err = lock.Close()
	if err != nil {
		t.Errorf("failed to release lock: %s", err)
		return
	}

	// wait for the notification to be delivered
	time.Sleep(2 * time.Second)

	if ctrl.notified.Load() == 0 {
		t.Errorf("expected lock release notification, but controller was not notified")
	}
}

func Test_LockCloseLeavesTakenOverLock(t *testing.T) {
	s := ephemeralTestStore(t, true)
	tbl, err := LocateLockTable[lockKey](s, "lock-test-"+uuid.NewString())
	if err != nil {
		t.Fatalf("failed to locate lock table: %s", err)
	}

	// a lock still held is released by Close
	held := &lockKey{Scope: "scope-1", Name: "held"}
	lock, err := tbl.TryAcquire(context.Background(), held)
	if err != nil {
		t.Fatalf("failed to acquire lock: %s", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close of a held lock: %s", err)
	}
	again, err := tbl.TryAcquire(context.Background(), held)
	if err != nil {
		t.Errorf("lock not released by close: %s", err)
	} else {
		_ = again.Close()
	}

	key := &lockKey{Scope: "scope-1", Name: "taken-over"}
	lock, err = tbl.TryAcquire(context.Background(), key)
	if err != nil {
		t.Fatalf("failed to acquire lock: %s", err)
	}

	// owner cleanup releases this process's lock, and another owner then
	// acquires the same key
	if err := tbl.col.DeleteOne(context.Background(), key); err != nil {
		t.Fatalf("failed to delete lock: %s", err)
	}
	other := fakeOwner(t)
	defer func() { _ = ownerTable.col.DeleteOne(context.Background(), &ownerKey{Name: other}) }()
	err = tbl.col.InsertOne(context.Background(), key, &lockData{CreateTime: time.Now().Unix(), Owner: other})
	if err != nil {
		t.Fatalf("failed to acquire lock as %s: %s", other, err)
	}
	defer func() { _ = tbl.col.DeleteOne(context.Background(), key) }()

	err = lock.Close()
	if !errors.IsNotFound(err) {
		t.Errorf("late close: got %v, want NotFound", err)
	}
	data := &lockData{}
	if err := tbl.col.FindOne(context.Background(), key, data); err != nil {
		t.Fatalf("the other owner's lock is gone after a late close: %s", err)
	}
	if data.Owner != other {
		t.Errorf("lock held by %q, want %q", data.Owner, other)
	}
}
