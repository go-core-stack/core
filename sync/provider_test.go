// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"testing"
	"time"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
)

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

	provider, err := tbl.CreateProvider(context.Background(), "test-key")
	if err != nil {
		t.Errorf("failed to create Provider: %s", err)
	}
	providerDup, err := tbl.CreateProvider(context.Background(), "test-key")
	if err != nil {
		t.Errorf("failed to create Provider: %s", err)
	}

	provider1, err := tbl.CreateProvider(context.Background(), "test-key-1")
	if err != nil {
		t.Errorf("failed to create provider: %s", err)
	}
	_ = provider1.Close()
	_ = providerDup.Close()
	_ = provider.Close()

	time.Sleep(2 * time.Second)
}
