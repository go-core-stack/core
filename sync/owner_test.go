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

func Test_OwnerInit(t *testing.T) {
	ctx, cancelfn := context.WithCancel(context.Background())
	defer time.Sleep(2 * time.Second)
	defer cancelfn()
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

	err = client.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("failed to perform Health check with DB Error: %s", err)
	}

	s := client.GetDataStore("test-sync")
	err = InitializeLockOwner(ctx, s, "test-owner")
	if err != nil && !errors.IsAlreadyExists(err) {
		t.Errorf("Got error while initializing lock owner %s", err)
	}
	time.Sleep(1 * time.Second)
}
