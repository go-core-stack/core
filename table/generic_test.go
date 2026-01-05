// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
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

type ProductKey struct {
	ID string
}

type Product struct {
	Name      string
	Price     int
	Category  string
	Stock     int
	CreatedAt time.Time
}

type ProductTable struct {
	Table[ProductKey, Product]
}

var (
	productTable *ProductTable
)

func initProductTable() {
	if productTable != nil {
		return
	}
	productTable = &ProductTable{}

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

	col := s.GetCollection("products-table")

	err = productTable.Initialize(col)
	if err != nil {
		log.Panicf("failed to initialize product table")
	}
}

func Test_GenericTable(t *testing.T) {
	initProductTable()

	t.Run("test_basic_crud", func(t *testing.T) {
		ctx := context.Background()

		key := &ProductKey{ID: "prod-001"}
		product := &Product{
			Name:      "Laptop",
			Price:     1200,
			Category:  "Electronics",
			Stock:     10,
			CreatedAt: time.Now(),
		}

		err := productTable.Insert(ctx, key, product)
		if err != nil {
			t.Errorf("failed to insert product: %s", err)
		}

		found, err := productTable.Find(ctx, key)
		if err != nil {
			t.Errorf("failed to find product: %s", err)
		}
		if found.Name != "Laptop" {
			t.Errorf("expected name 'Laptop', got '%s'", found.Name)
		}

		// Update
		product.Price = 1100
		err = productTable.Update(ctx, key, product)
		if err != nil {
			t.Errorf("failed to update product: %s", err)
		}

		found, err = productTable.Find(ctx, key)
		if err != nil {
			t.Errorf("failed to find updated product: %s", err)
		}
		if found.Price != 1100 {
			t.Errorf("expected price 1100, got %d", found.Price)
		}

		// Cleanup
		err = productTable.DeleteKey(ctx, key)
		if err != nil {
			t.Errorf("failed to delete product: %s", err)
		}
	})

	t.Run("test_sorting_single_field_ascending", func(t *testing.T) {
		ctx := context.Background()

		// Insert test products
		testProducts := []struct {
			id       string
			name     string
			price    int
			category string
			stock    int
		}{
			{"sort-test-1", "Keyboard", 50, "Electronics", 20},
			{"sort-test-2", "Mouse", 25, "Electronics", 30},
			{"sort-test-3", "Monitor", 300, "Electronics", 15},
			{"sort-test-4", "Desk", 200, "Furniture", 10},
			{"sort-test-5", "Chair", 150, "Furniture", 25},
		}

		for _, tp := range testProducts {
			key := &ProductKey{ID: tp.id}
			product := &Product{
				Name:      tp.name,
				Price:     tp.price,
				Category:  tp.category,
				Stock:     tp.stock,
				CreatedAt: time.Now(),
			}
			err := productTable.Insert(ctx, key, product)
			if err != nil {
				t.Errorf("failed to insert test product %s: %s", tp.id, err)
			}
		}

		// Test sorting by price ascending
		results, err := productTable.FindManyWithOpts(ctx, nil,
			WithLimit(10),
			WithSort(SortOption{Field: "price", Direction: SortAscending}))
		if err != nil {
			t.Errorf("failed to find products with ascending sort: %s", err)
		}

		if len(results) != 5 {
			t.Errorf("expected 5 products, got %d", len(results))
		}

		// Verify order
		if results[0].Price != 25 {
			t.Errorf("expected first product price to be 25, got %d", results[0].Price)
		}
		if results[len(results)-1].Price != 300 {
			t.Errorf("expected last product price to be 300, got %d", results[len(results)-1].Price)
		}

		// Cleanup
		count, err := productTable.DeleteByFilter(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to cleanup test products: %s", err)
		}
		if count != 5 {
			t.Errorf("expected to delete 5 products, deleted %d", count)
		}
	})

	t.Run("test_sorting_single_field_descending", func(t *testing.T) {
		ctx := context.Background()

		// Insert test products
		testProducts := []struct {
			id    string
			name  string
			stock int
		}{
			{"stock-test-1", "Product A", 100},
			{"stock-test-2", "Product B", 50},
			{"stock-test-3", "Product C", 200},
		}

		for _, tp := range testProducts {
			key := &ProductKey{ID: tp.id}
			product := &Product{
				Name:      tp.name,
				Stock:     tp.stock,
				CreatedAt: time.Now(),
			}
			err := productTable.Insert(ctx, key, product)
			if err != nil {
				t.Errorf("failed to insert test product: %s", err)
			}
		}

		// Sort by stock descending
		results, err := productTable.FindManyWithOpts(ctx, nil,
			WithLimit(10),
			WithSort(SortOption{Field: "stock", Direction: SortDescending}))
		if err != nil {
			t.Errorf("failed to find products with descending sort: %s", err)
		}

		if len(results) != 3 {
			t.Errorf("expected 3 products, got %d", len(results))
		}

		if results[0].Stock != 200 {
			t.Errorf("expected first stock to be 200, got %d", results[0].Stock)
		}
		if results[2].Stock != 50 {
			t.Errorf("expected last stock to be 50, got %d", results[2].Stock)
		}

		// Cleanup
		count, err := productTable.DeleteByFilter(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to cleanup: %s", err)
		}
		if count != 3 {
			t.Errorf("expected to delete 3 products, deleted %d", count)
		}
	})

	t.Run("test_sorting_multiple_fields", func(t *testing.T) {
		ctx := context.Background()

		// Insert products with same category but different prices
		testProducts := []struct {
			id       string
			name     string
			category string
			price    int
		}{
			{"multi-1", "Item A", "Books", 30},
			{"multi-2", "Item B", "Books", 20},
			{"multi-3", "Item C", "Electronics", 100},
			{"multi-4", "Item D", "Electronics", 50},
			{"multi-5", "Item E", "Books", 25},
		}

		for _, tp := range testProducts {
			key := &ProductKey{ID: tp.id}
			product := &Product{
				Name:      tp.name,
				Category:  tp.category,
				Price:     tp.price,
				CreatedAt: time.Now(),
			}
			err := productTable.Insert(ctx, key, product)
			if err != nil {
				t.Errorf("failed to insert test product: %s", err)
			}
		}

		// Sort by category ascending, then price ascending
		results, err := productTable.FindManyWithOpts(ctx, nil,
			WithLimit(10),
			WithSort(
				SortOption{Field: "category", Direction: SortAscending},
				SortOption{Field: "price", Direction: SortAscending},
			))
		if err != nil {
			t.Errorf("failed to find products with multi-field sort: %s", err)
		}

		if len(results) != 5 {
			t.Errorf("expected 5 products, got %d", len(results))
		}

		// First three should be Books (ascending alphabetically), sorted by price
		if results[0].Category != "Books" || results[0].Price != 20 {
			t.Errorf("expected first result: Books, $20, got %s, $%d", results[0].Category, results[0].Price)
		}
		if results[1].Category != "Books" || results[1].Price != 25 {
			t.Errorf("expected second result: Books, $25, got %s, $%d", results[1].Category, results[1].Price)
		}
		if results[2].Category != "Books" || results[2].Price != 30 {
			t.Errorf("expected third result: Books, $30, got %s, $%d", results[2].Category, results[2].Price)
		}

		// Last two should be Electronics, sorted by price
		if results[3].Category != "Electronics" || results[3].Price != 50 {
			t.Errorf("expected fourth result: Electronics, $50, got %s, $%d", results[3].Category, results[3].Price)
		}
		if results[4].Category != "Electronics" || results[4].Price != 100 {
			t.Errorf("expected fifth result: Electronics, $100, got %s, $%d", results[4].Category, results[4].Price)
		}

		// Cleanup
		count, err := productTable.DeleteByFilter(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to cleanup: %s", err)
		}
		if count != 5 {
			t.Errorf("expected to delete 5 products, deleted %d", count)
		}
	})

	t.Run("test_sorting_with_filter", func(t *testing.T) {
		ctx := context.Background()

		// Insert products with various prices
		testProducts := []struct {
			id    string
			name  string
			price int
		}{
			{"filter-1", "Cheap Item", 10},
			{"filter-2", "Mid Item 1", 50},
			{"filter-3", "Expensive Item", 200},
			{"filter-4", "Mid Item 2", 75},
			{"filter-5", "Budget Item", 30},
		}

		for _, tp := range testProducts {
			key := &ProductKey{ID: tp.id}
			product := &Product{
				Name:      tp.name,
				Price:     tp.price,
				CreatedAt: time.Now(),
			}
			err := productTable.Insert(ctx, key, product)
			if err != nil {
				t.Errorf("failed to insert test product: %s", err)
			}
		}

		// Filter products with price >= 50, sort by price ascending
		filter := bson.D{{Key: "price", Value: bson.D{{Key: "$gte", Value: 50}}}}
		results, err := productTable.FindManyWithOpts(ctx, filter,
			WithLimit(10),
			WithSort(SortOption{Field: "price", Direction: SortAscending}))
		if err != nil {
			t.Errorf("failed to find products with filter and sort: %s", err)
		}

		if len(results) != 3 {
			t.Errorf("expected 3 products with price >= 50, got %d", len(results))
		}

		// Verify sorted order
		if results[0].Price != 50 {
			t.Errorf("expected first price to be 50, got %d", results[0].Price)
		}
		if results[1].Price != 75 {
			t.Errorf("expected second price to be 75, got %d", results[1].Price)
		}
		if results[2].Price != 200 {
			t.Errorf("expected third price to be 200, got %d", results[2].Price)
		}

		// Cleanup
		count, err := productTable.DeleteByFilter(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to cleanup: %s", err)
		}
		if count != 5 {
			t.Errorf("expected to delete 5 products, deleted %d", count)
		}
	})

	t.Run("test_sorting_with_pagination", func(t *testing.T) {
		ctx := context.Background()

		// Insert 10 products with sequential prices
		for i := 1; i <= 10; i++ {
			key := &ProductKey{ID: "page-" + string(rune('0'+i))}
			product := &Product{
				Name:      "Product " + string(rune('0'+i)),
				Price:     i * 10,
				CreatedAt: time.Now(),
			}
			err := productTable.Insert(ctx, key, product)
			if err != nil {
				t.Errorf("failed to insert product %d: %s", i, err)
			}
		}

		// Get second page (skip 3, limit 3) sorted by price ascending
		results, err := productTable.FindManyWithOpts(ctx, nil,
			WithOffset(3),
			WithLimit(3),
			WithSort(SortOption{Field: "price", Direction: SortAscending}))
		if err != nil {
			t.Errorf("failed to find products with pagination: %s", err)
		}

		if len(results) != 3 {
			t.Errorf("expected 3 products in page, got %d", len(results))
		}

		// Should get products with prices 40, 50, 60 (4th, 5th, 6th items)
		if results[0].Price != 40 {
			t.Errorf("expected first price to be 40, got %d", results[0].Price)
		}
		if results[1].Price != 50 {
			t.Errorf("expected second price to be 50, got %d", results[1].Price)
		}
		if results[2].Price != 60 {
			t.Errorf("expected third price to be 60, got %d", results[2].Price)
		}

		// Cleanup
		count, err := productTable.DeleteByFilter(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to cleanup: %s", err)
		}
		if count != 10 {
			t.Errorf("expected to delete 10 products, deleted %d", count)
		}
	})

	t.Run("test_no_sorting", func(t *testing.T) {
		ctx := context.Background()

		// Insert a few products
		for i := 1; i <= 3; i++ {
			key := &ProductKey{ID: "no-sort-" + string(rune('0'+i))}
			product := &Product{
				Name:      "Product " + string(rune('0'+i)),
				Price:     i * 100,
				CreatedAt: time.Now(),
			}
			err := productTable.Insert(ctx, key, product)
			if err != nil {
				t.Errorf("failed to insert product: %s", err)
			}
		}

		// Query without sorting using original FindMany (backwards compatibility)
		results, err := productTable.FindMany(ctx, nil, 0, 10)
		if err != nil {
			t.Errorf("failed to find products without sort: %s", err)
		}

		if len(results) != 3 {
			t.Errorf("expected 3 products, got %d", len(results))
		}

		// Query with FindManyWithOpts with just limit (no offset, no sort)
		results, err = productTable.FindManyWithOpts(ctx, nil, WithLimit(10))
		if err != nil {
			t.Errorf("failed to find products with just limit: %s", err)
		}

		if len(results) != 3 {
			t.Errorf("expected 3 products, got %d", len(results))
		}

		// Query with FindManyWithOpts with no options at all
		results, err = productTable.FindManyWithOpts(ctx, nil)
		if err != nil {
			t.Errorf("failed to find products with no options: %s", err)
		}

		if len(results) != 3 {
			t.Errorf("expected 3 products, got %d", len(results))
		}

		// Cleanup
		count, err := productTable.DeleteByFilter(ctx, bson.D{})
		if err != nil {
			t.Errorf("failed to cleanup: %s", err)
		}
		if count != 3 {
			t.Errorf("expected to delete 3 products, deleted %d", count)
		}
	})
}
