// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"testing"
)

func Test_ClientConnection(t *testing.T) {
	config := &MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}

	client, err := NewMongoClient(config)

	if err != nil {
		t.Errorf("failed to connect to mongo DB Error: %s", err)
		return
	}

	err = client.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("failed to perform Health check with DB Error: %s", err)
	}

	_ = client.GetDataStore("test")
}
