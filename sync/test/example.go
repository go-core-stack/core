package main

import (
	"context"
	"log"
	"time"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
	"github.com/Prabhjot-Sethi/core/sync"
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
	err = sync.InitializeLockOwnerWithUpdateInterval(ctx, s, "test-owner", 1)
	if err != nil && !errors.IsAlreadyExists(err) {
		log.Panicf("Got error while initializing lock owner %s", err)
	}
	for {
		// loop endlessly to run aging process for owner table
		time.Sleep(5 * time.Second)
	}
}
