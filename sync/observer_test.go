// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
	"github.com/Prabhjot-Sethi/core/reconciler"
)

type MyObserver struct {
	reconciler.Controller
	providers map[string]struct{}
	tbl       *ProviderTable
}

func (o *MyObserver) Reconcile(k any) (*reconciler.Result, error) {
	key := k.(string)
	if key == "" {
		log.Panicln("Got invalid key response")
	}
	if o.tbl.IsProviderAvailable(key) {
		o.providers[key] = struct{}{}
	} else {
		delete(o.providers, key)
	}
	return &reconciler.Result{}, nil
}

func Test_ObserverBaseTesting(t *testing.T) {
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

	obs := &MyObserver{
		providers: map[string]struct{}{},
		tbl:       tbl,
	}
	_ = tbl.Register("test-observer", obs)

	provider, err := tbl.CreateProvider(context.Background(), "test-key")
	if err != nil {
		t.Errorf("failed to create Provider: %s", err)
	}
	time.Sleep(1 * time.Second)
	if len(obs.providers) != 1 {
		t.Errorf("Expected 1 provider but got %d", len(obs.providers))
	}

	providerDup, err := tbl.CreateProvider(context.Background(), "test-key")
	if err != nil {
		t.Errorf("failed to create Provider: %s", err)
	}
	time.Sleep(1 * time.Second)
	if len(obs.providers) != 1 {
		t.Errorf("Expected 1 provider but got %d", len(obs.providers))
	}

	provider1, err := tbl.CreateProvider(context.Background(), "test-key-1")
	if err != nil {
		t.Errorf("failed to create provider: %s", err)
	}
	time.Sleep(1 * time.Second)
	if len(obs.providers) != 2 {
		t.Errorf("Expected 2 provider but got %d", len(obs.providers))
	}
	_ = provider1.Close()
	_ = providerDup.Close()
	_ = provider.Close()

	time.Sleep(2 * time.Second)
	if len(obs.providers) != 0 {
		t.Errorf("Expected 0 provider but got %d", len(obs.providers))
	}
}
