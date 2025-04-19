// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"testing"
	"time"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
)

type myProviderKey struct {
	Scope string
	Name  string
}

func Test_ProviderBaseTesting(t *testing.T) {
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
		t.Errorf("Got error while initializing sync owner %s", err)
	}

	tbl, err := LocateProviderTable(s)

	if err != nil {
		t.Errorf("failed to locate provider Table: %s", err)
	}

	key1 := &myProviderKey{
		Scope: "scope-1",
		Name:  "test-key",
	}

	provider, err := tbl.CreateProvider(context.Background(), key1)
	if err != nil {
		t.Errorf("failed to create Provider: %s", err)
	}
	_, err = tbl.CreateProvider(context.Background(), key1)
	if err != nil {
		t.Errorf("failed to create Provider: %s", err)
	}
	key2 := &myProviderKey{
		Scope: "scope-2",
		Name:  "test-key",
	}

	provider1, err := tbl.CreateProvider(context.Background(), key2)
	if err != nil {
		t.Errorf("failed to acquire lock: %s", err)
	}
	_ = provider1.Close()
	_ = provider.Close()

	time.Sleep(2 * time.Second)
}
