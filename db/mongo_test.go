// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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

	t.Run("UpdateOne_NonExistent_Without_Upsert", func(t *testing.T) {
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

		// Use a key that definitely doesn't exist
		nonExistentKey := &MyKey{
			Name: "non-existent-key-for-update-test",
		}
		data := &MyData{
			Desc: "test-description",
			Val: &InternaData{
				Test: "test-value",
			},
		}

		// Ensure the key doesn't exist by attempting to delete it
		_ = col.DeleteOne(context.Background(), nonExistentKey)

		// Try to update a non-existent document without upsert
		// This should return a NotFound error
		err = col.UpdateOne(context.Background(), nonExistentKey, data, false)
		if err == nil {
			t.Errorf("expected NotFound error when updating non-existent document without upsert, but got nil")
		}

		// Verify that no document was created
		val := &MyData{}
		err = col.FindOne(context.Background(), nonExistentKey, val)
		if err == nil {
			t.Errorf("expected NotFound error when finding document that should not exist, but got nil")
		}
	})

	t.Run("Find_many_with_offset_limits", func(t *testing.T) {
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

		key1 := &MyKey{
			Name: "test-key-1",
		}

		key2 := &MyKey{
			Name: "test-key-2",
		}

		err = col.InsertOne(context.Background(), key, data)
		if err != nil {
			t.Errorf("failed to insert an entry to collection Error: %s", err)
		}

		err = col.InsertOne(context.Background(), key1, data)
		if err != nil {
			t.Errorf("failed to insert an entry to collection Error: %s", err)
		}

		err = col.InsertOne(context.Background(), key2, data)
		if err != nil {
			t.Errorf("failed to insert an entry to collection Error: %s", err)
		}

		val := &MyData{}
		err = col.FindOne(context.Background(), key2, val)
		if err != nil {
			t.Errorf("failed to find the entry Error: %s", err)
		}
		fmt.Printf("found entry :%v", *val)

		list := []*MyData{}
		opts := options.Find().SetLimit(2).SetSkip(2)
		err = col.FindMany(context.Background(), nil, &list, opts)
		if err != nil {
			t.Errorf("failed to find the entry Error: %s", err)
		}

		if len(list) != 1 {
			t.Errorf("Expected 1 entries in table but got %d", len(list))
		}

		list = []*MyData{}
		opts = options.Find().SetLimit(2).SetSkip(0)
		err = col.FindMany(context.Background(), nil, &list, opts)
		if err != nil {
			t.Errorf("failed to find the entry Error: %s", err)
		}

		if len(list) != 2 {
			t.Errorf("Expected 2 entries in table but got %d", len(list))
		}

		err = col.DeleteOne(context.Background(), key)
		if err != nil {
			t.Errorf("failed to delete entry using key Error: %s", err)
		}

		err = col.DeleteOne(context.Background(), key1)
		if err != nil {
			t.Errorf("failed to delete entry using key Error: %s", err)
		}

		err = col.DeleteOne(context.Background(), key2)
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

var (
	mongoTestAddUpOps    int
	mongoTestDeleteOps   int
	myMongoTestDeleteOps int
)

func myKeyWatcher(op string, wKey interface{}) {
	_ = wKey.(*MyKey)
	switch op {
	case MongoAddOp, MongoUpdateOp:
		mongoTestAddUpOps += 1
	case MongoDeleteOp:
		mongoTestDeleteOps += 1
	}
}

func myDeleteWatcher(op string, wKey interface{}) {
	_ = wKey.(*MyKey)
	myMongoTestDeleteOps += 1
}

func Test_CollectionWatch(t *testing.T) {
	t.Run("WatchTest", func(t *testing.T) {
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

		err = col.SetKeyType(reflect.TypeOf(MyKey{}))
		if err == nil {
			t.Errorf("collection should not allow key type, when not a pointer")
		}

		// set key type to ptr of my key
		err = col.SetKeyType(reflect.TypeOf(&MyKey{}))
		if err != nil {
			t.Errorf("failed to set key type for watch: %s", err)
		}

		watchCtx, cancelfn := context.WithCancel(context.Background())
		defer func() {
			time.Sleep(2 * time.Second)
			cancelfn()
			if mongoTestAddUpOps != 3 {
				t.Errorf("Add/Update Notify: Got %d, expected 3", mongoTestAddUpOps)
			}
			if mongoTestDeleteOps != 2 {
				t.Errorf("Delete Notify: Got %d, expected 2", mongoTestDeleteOps)
			}
			if myMongoTestDeleteOps != 2 {
				t.Errorf("expected delete count %d, but got %d", 2, myMongoTestDeleteOps)
			}
		}()
		// reset counters
		mongoTestAddUpOps = 0
		mongoTestDeleteOps = 0
		myMongoTestDeleteOps = 0
		_ = col.Watch(watchCtx, nil, myKeyWatcher)
		matchDeleteStage := mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "operationType", Value: "delete"}}}}}
		_ = col.Watch(watchCtx, matchDeleteStage, myDeleteWatcher)

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

		count, err := col.Count(context.Background(), nil)
		if err != nil {
			t.Errorf("failed to get count of entries in collection: %s", err)
		}

		if count != 1 {
			t.Errorf("Expected 1 entries in table but got %d", count)
		}

		err = col.DeleteOne(context.Background(), key)
		if err != nil {
			t.Errorf("failed to delete entry using key Error: %s", err)
		}
	})
}
