// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/go-core-stack/core/db"
	"go.mongodb.org/mongo-driver/v2/bson"
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

type MyTable struct {
	CachedTable[MyKey, MyData]
}

var (
	myTable *MyTable
)

func clientInit() {
	if myTable != nil {
		return
	}
	myTable = &MyTable{}

	config := &db.MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}

	client, err := db.NewMongoClient(config)

	if err != nil {
		log.Panicf("failed to connect to mongo DB Error: %s", err)
	}

	err = client.HealthCheck(context.Background())
	if err != nil {
		log.Panicf("failed to perform Health check with DB Error: %s", err)
	}

	s := client.GetDataStore("test")

	col := s.GetCollection("my-cached-table")

	err = myTable.Initialize(col)
	if err != nil {
		log.Panicf("failed to initialize cached table")
	}
}

func Test_CachedClient(t *testing.T) {
	clientInit()
	t.Run("push_and_find_entries", func(t *testing.T) {

		key := &MyKey{
			Name: "test-key-1",
		}
		data := &MyData{
			Desc: "sample-description-1",
			Val: &InternaData{
				Test: "abc-1",
			},
		}

		ctx := context.Background()

		err := myTable.Insert(ctx, key, data)
		if err != nil {
			t.Errorf("failed inserting entry to the table, got error: %s", err)
		}

		// second insert with same key should fail
		err = myTable.Insert(ctx, key, data)
		if err == nil {
			t.Errorf("second insert for same entry to the table succeeded, expeted error")
		}

		key2 := &MyKey{
			Name: "test-key-2",
		}
		data2 := &MyData{
			Desc: "sample-description-2",
			Val: &InternaData{
				Test: "abc-2",
			},
		}

		err = myTable.Insert(ctx, key2, data2)
		if err != nil {
			t.Errorf("failed inserting second entry to the table, got error: %s", err)
		}

		// add a sleep timer to ensure that the processing of the context is completed
		// for the cache table
		time.Sleep(1 * time.Second)

		entry, err := myTable.Find(ctx, key)
		if err != nil {
			t.Errorf("failed to find the inserted entry from the table, got error: %s", err)
		} else {
			if entry.Desc != "sample-description-1" {
				t.Errorf("expected sample-description-1, but got %s", entry.Desc)
			}
		}

		entry, err = myTable.Find(ctx, key2)
		if err != nil {
			t.Errorf("failed to find the inserted entry from the table, got error: %s", err)
		} else {
			if entry.Desc != "sample-description-2" {
				t.Errorf("expected sample-description-2, but got %s", entry.Desc)
			}
		}

		count, err := myTable.col.DeleteMany(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to delete the entries from table, got error %s", err)
		} else {
			if count != 2 {
				t.Errorf("expected delete of two entries from table, but got %d", count)
			}
		}
	})

	t.Run("find_updated_result", func(t *testing.T) {

		key := &MyKey{
			Name: "test-key-1",
		}
		data := &MyData{
			Desc: "sample-description-1",
			Val: &InternaData{
				Test: "abc-1",
			},
		}

		ctx := context.Background()

		err := myTable.Insert(ctx, key, data)
		if err != nil {
			t.Errorf("failed inserting entry to the table, got error: %s", err)
		}

		// second insert with same key should fail
		err = myTable.Insert(ctx, key, data)
		if err == nil {
			t.Errorf("second insert for same entry to the table succeeded, expeted error")
		}

		key2 := &MyKey{
			Name: "test-key-2",
		}
		data2 := &MyData{
			Desc: "sample-description-2",
			Val: &InternaData{
				Test: "abc-2",
			},
		}

		err = myTable.Insert(ctx, key2, data2)
		if err != nil {
			t.Errorf("failed inserting second entry to the table, got error: %s", err)
		}

		// add a sleep timer to ensure that the processing of the context is completed
		// for the cache table
		time.Sleep(1 * time.Second)

		entry, err := myTable.Find(ctx, key)
		if err != nil {
			t.Errorf("failed to find the inserted entry from the table, got error: %s", err)
		} else {
			if entry.Desc != "sample-description-1" {
				t.Errorf("expected sample-description-1, but got %s", entry.Desc)
			}
		}

		// trigger update
		data3 := &MyData{
			Desc: "sample-description-3",
			Val: &InternaData{
				Test: "abc-1",
			},
		}

		err = myTable.Update(ctx, key, data3)
		if err != nil {
			t.Errorf("failed to update data into cached table, got error: %s", err)
		}

		// add a sleep timer to ensure that the processing of the context is completed
		// for the cache table
		time.Sleep(1 * time.Second)

		entry, err = myTable.Find(ctx, key)
		if err != nil {
			t.Errorf("failed to find the inserted entry from the table, got error: %s", err)
		} else {
			if entry.Desc != "sample-description-3" {
				t.Errorf("expected sample-description-3, but got %s", entry.Desc)
			}
		}

		count, err := myTable.col.DeleteMany(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to delete the entries from table, got error %s", err)
		} else {
			if count != 2 {
				t.Errorf("expected delete of two entries from table, but got %d", count)
			}
		}
	})
}
