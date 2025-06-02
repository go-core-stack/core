// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package main

import (
	"context"
	"log"
	"time"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/sync"
)

func main() {
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
		log.Panicf("failed to connect to mongo DB Error: %s", err)
		return
	}

	err = client.HealthCheck(context.Background())
	if err != nil {
		log.Panicf("failed to perform Health check with DB Error: %s", err)
	}

	s := client.GetDataStore("test-sync")
	err = sync.InitializeOwnerWithUpdateInterval(ctx, s, "test-owner", 10)
	if err != nil && !errors.IsAlreadyExists(err) {
		log.Panicf("Got error while initializing sync owner %s", err)
	}

	_, err = sync.LocateProviderTable(s)

	if err != nil {
		log.Panicf("failed to locate provider Table: %s", err)
	}

	for {
		// loop endlessly to run aging process for owner table
		time.Sleep(5 * time.Second)
	}
}
