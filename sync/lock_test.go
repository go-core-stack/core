// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"testing"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
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
}
