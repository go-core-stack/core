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
	Desc  string
	Val   *InternaData
	Score int
	Order int
}

type MyTable struct {
	CachedTable[MyKey, MyData]
}

var (
	myTable *MyTable

	myTable2 *MyTable
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

func clientInitTable2() {
	if myTable2 != nil {
		return
	}
	myTable2 = &MyTable{}

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

	err = myTable2.Initialize(col)
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

		clientInitTable2()
		time.Sleep(1 * time.Second)
		entry, err = myTable2.Find(ctx, key2)
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

	t.Run("test_sorting_functionality", func(t *testing.T) {
		ctx := context.Background()

		// Insert multiple entries with different scores and orders
		testData := []struct {
			name  string
			desc  string
			score int
			order int
		}{
			{"test-sort-1", "first", 100, 3},
			{"test-sort-2", "second", 200, 1},
			{"test-sort-3", "third", 150, 2},
			{"test-sort-4", "fourth", 200, 4},
			{"test-sort-5", "fifth", 50, 5},
		}

		// Insert all test entries
		for _, td := range testData {
			key := &MyKey{Name: td.name}
			data := &MyData{
				Desc:  td.desc,
				Score: td.score,
				Order: td.order,
				Val:   &InternaData{Test: "test"},
			}
			err := myTable.Insert(ctx, key, data)
			if err != nil {
				t.Errorf("failed inserting test data %s: %s", td.name, err)
			}
		}

		// Wait for cache to update
		time.Sleep(1 * time.Second)

		// Test 1: Sort by score ascending
		results, err := myTable.DBFindManyWithOpts(ctx, nil,
			WithLimit(10),
			WithSort(SortOption{Field: "score", Direction: SortAscending}))
		if err != nil {
			t.Errorf("failed to find entries with ascending sort: %s", err)
		}
		if len(results) != 5 {
			t.Errorf("expected 5 results, got %d", len(results))
		}
		if results[0].Score != 50 {
			t.Errorf("expected first score to be 50, got %d", results[0].Score)
		}
		if results[len(results)-1].Score != 200 {
			t.Errorf("expected last score to be 200, got %d", results[len(results)-1].Score)
		}

		// Test 2: Sort by score descending
		results, err = myTable.DBFindManyWithOpts(ctx, nil,
			WithLimit(10),
			WithSort(SortOption{Field: "score", Direction: SortDescending}))
		if err != nil {
			t.Errorf("failed to find entries with descending sort: %s", err)
		}
		if len(results) != 5 {
			t.Errorf("expected 5 results, got %d", len(results))
		}
		if results[0].Score != 200 {
			t.Errorf("expected first score to be 200, got %d", results[0].Score)
		}
		if results[len(results)-1].Score != 50 {
			t.Errorf("expected last score to be 50, got %d", results[len(results)-1].Score)
		}

		// Test 3: Multi-field sort (score descending, then order ascending)
		results, err = myTable.DBFindManyWithOpts(ctx, nil,
			WithLimit(10),
			WithSort(
				SortOption{Field: "score", Direction: SortDescending},
				SortOption{Field: "order", Direction: SortAscending},
			))
		if err != nil {
			t.Errorf("failed to find entries with multi-field sort: %s", err)
		}
		if len(results) != 5 {
			t.Errorf("expected 5 results, got %d", len(results))
		}
		// First two should have score 200, with order 1 before order 4
		if results[0].Score != 200 || results[0].Order != 1 {
			t.Errorf("expected first result to have score=200, order=1, got score=%d, order=%d", results[0].Score, results[0].Order)
		}
		if results[1].Score != 200 || results[1].Order != 4 {
			t.Errorf("expected second result to have score=200, order=4, got score=%d, order=%d", results[1].Score, results[1].Order)
		}

		// Test 4: No sorting (just limit, no sort options)
		results, err = myTable.DBFindManyWithOpts(ctx, nil, WithLimit(10))
		if err != nil {
			t.Errorf("failed to find entries with no sort: %s", err)
		}
		if len(results) != 5 {
			t.Errorf("expected 5 results with no sort, got %d", len(results))
		}

		// Test 5: Sorting with filter
		filter := bson.D{{Key: "score", Value: bson.D{{Key: "$gte", Value: 150}}}}
		results, err = myTable.DBFindManyWithOpts(ctx, filter,
			WithLimit(10),
			WithSort(SortOption{Field: "score", Direction: SortAscending}))
		if err != nil {
			t.Errorf("failed to find entries with filter and sort: %s", err)
		}
		if len(results) != 3 {
			t.Errorf("expected 3 results with filter, got %d", len(results))
		}
		if results[0].Score != 150 {
			t.Errorf("expected first filtered result to have score=150, got %d", results[0].Score)
		}

		// Test 6: Sorting with pagination
		results, err = myTable.DBFindManyWithOpts(ctx, nil,
			WithOffset(1),
			WithLimit(2),
			WithSort(SortOption{Field: "order", Direction: SortAscending}))
		if err != nil {
			t.Errorf("failed to find entries with pagination and sort: %s", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results with pagination, got %d", len(results))
		}
		// Should skip first (order=1) and return next two (order=2,3)
		if results[0].Order != 2 {
			t.Errorf("expected first paginated result to have order=2, got %d", results[0].Order)
		}
		if results[1].Order != 3 {
			t.Errorf("expected second paginated result to have order=3, got %d", results[1].Order)
		}

		// Cleanup: delete all test entries
		count, err := myTable.col.DeleteMany(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to delete test entries: %s", err)
		}
		if count != 5 {
			t.Errorf("expected to delete 5 entries, deleted %d", count)
		}
	})
}
