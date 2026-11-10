// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"context"
	"log"
	gosync "sync"
	"testing"
	"time"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

// MyObserver records the providers it is told about. Reconcile runs on the
// reconciler's goroutine and the test reads from its own, so mu guards
// providers.
type MyObserver struct {
	reconciler.Controller
	mu        gosync.Mutex
	providers map[string]struct{}
	tbl       *ProviderTable
}

func (o *MyObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.providers)
}

func (o *MyObserver) Reconcile(k any) (*reconciler.Result, error) {
	key := k.(string)
	if key == "" {
		log.Panicln("Got invalid key response")
	}
	available := o.tbl.IsProviderAvailable(key)
	o.mu.Lock()
	defer o.mu.Unlock()
	if available {
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
	if obs.count() != 1 {
		t.Errorf("Expected 1 provider but got %d", obs.count())
	}

	providerDup, err := tbl.CreateProvider(context.Background(), "test-key")
	if err != nil {
		t.Errorf("failed to create Provider: %s", err)
	}
	time.Sleep(1 * time.Second)
	if obs.count() != 1 {
		t.Errorf("Expected 1 provider but got %d", obs.count())
	}

	provider1, err := tbl.CreateProvider(context.Background(), "test-key-1")
	if err != nil {
		t.Errorf("failed to create provider: %s", err)
	}
	time.Sleep(1 * time.Second)
	if obs.count() != 2 {
		t.Errorf("Expected 2 provider but got %d", obs.count())
	}
	_ = provider1.Close()
	_ = providerDup.Close()
	_ = provider.Close()

	time.Sleep(2 * time.Second)
	if obs.count() != 0 {
		t.Errorf("Expected 0 provider but got %d", obs.count())
	}
}
