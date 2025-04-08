// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"testing"
)

func Test_ClientConnection(t *testing.T) {
	t.Run("Valid_Auth_Config", func(t *testing.T) {
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
	})

	t.Run("InValid_Port", func(t *testing.T) {
		config := &MongoConfig{
			Host:     "localhost",
			Port:     "abc",
			Username: "root",
			Password: "badPassword",
		}
		_, err := NewMongoClient(config)

		if err == nil {
			t.Errorf("Connection succeeded while using invalid port number")
			return
		}
	})

	t.Run("InValid_Auth_Config", func(t *testing.T) {
		config := &MongoConfig{
			Host:     "localhost",
			Port:     "27017",
			Username: "root",
			Password: "badPassword",
		}
		client, err := NewMongoClient(config)

		if err != nil {
			t.Errorf("failed to connect to mongo DB Error: %s", err)
			return
		}

		err = client.HealthCheck(context.Background())
		if err == nil {
			t.Errorf("Health Check for mongo DB passed while using wrong password")
		}
	})
}
