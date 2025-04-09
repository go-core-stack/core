// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"testing"
)

type MyKey struct {
	Name string
}

type InternaData struct {
	Test string
}

type MyData struct {
	Desc string
	Val  *InternaData
}

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

		s := client.GetDataStore("test")

		col := s.GetCollection("collection1")

		key := &MyKey{
			Name: "test-key",
		}
		data := &MyData{
			Desc: "sample-description",
			Val: &InternaData{
				Test: "abc",
			},
		}

		err = col.InsertOne(context.Background(), key, data)
		if err != nil {
			t.Errorf("failed to insert an entry to collection Error: %s", err)
		}

		val := &MyData{}
		err = col.FindOne(context.Background(), key, val)
		if err != nil {
			t.Errorf("failed to find the entry Error: %s", err)
		}

		data.Desc = "new description"
		data.Val.Test = "xyz"
		err = col.UpdateOne(context.Background(), key, data, false)
		if err != nil {
			t.Errorf("failed to update an entry to collection Error: %s", err)
		}

		val = &MyData{}
		err = col.FindOne(context.Background(), key, val)
		if err != nil {
			t.Errorf("failed to find the entry Error: %s", err)
		}

		err = col.DeleteOne(context.Background(), key)
		if err != nil {
			t.Errorf("failed to delete entry using key Error: %s", err)
		}

		err = col.DeleteOne(context.Background(), key)
		if err == nil {
			t.Errorf("attemptting delete on already deleted entry, but didn't receive expected error")
		}

		err = col.UpdateOne(context.Background(), key, data, true)
		if err != nil {
			t.Errorf("failed to update an entry to collection Error: %s", err)
		}

		err = col.DeleteOne(context.Background(), key)
		if err != nil {
			t.Errorf("failed to delete entry using key Error: %s", err)
		}
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
