// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
)

// readHookCol runs beforeRead ahead of each FindOne and afterRead after
// each one that succeeds, fails FindOne with err while err is set, runs
// the read through around while that is set, and fails FindMany with
// listErr while that is set; every other method is the wrapped
// collection's. It forwards Lifetime, so a table over it ends its
// reconciler and retrier when the client is closed.
type readHookCol struct {
	db.StoreCollection
	mu         sync.Mutex
	err        error
	listErr    error
	beforeRead func(key any)
	afterRead  func(key any)
	// around performs a FindOne in place of the plain read: read is the
	// plain read, and around may fail, hold or skip it
	around func(ctx context.Context, key any, read func() error) error
}

func (c *readHookCol) Lifetime() context.Context {
	return c.StoreCollection.(db.ClientLifetime).Lifetime()
}

func (c *readHookCol) setAround(fn func(ctx context.Context, key any, read func() error) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.around = fn
}

func (c *readHookCol) setListErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listErr = err
}

func (c *readHookCol) FindMany(ctx context.Context, filter any, data any, opts ...any) error {
	c.mu.Lock()
	err := c.listErr
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.StoreCollection.FindMany(ctx, filter, data, opts...)
}

func (c *readHookCol) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

func (c *readHookCol) FindOne(ctx context.Context, key any, data any) error {
	c.mu.Lock()
	err, before, after, around := c.err, c.beforeRead, c.afterRead, c.around
	c.mu.Unlock()
	if before != nil {
		before(key)
	}
	if err != nil {
		return err
	}
	read := func() error { return c.StoreCollection.FindOne(ctx, key, data) }
	if around != nil {
		if err := around(ctx, key, read); err != nil {
			return err
		}
	} else if err := read(); err != nil {
		return err
	}
	if after != nil {
		after(key)
	}
	return nil
}

// An entry that cannot be read during the eager load fails the start with
// the read's own error code and the key, instead of starting with that
// entry missing. The failed start leaves no watch running and the table
// uninitialized, so a second Initialize on it loads every entry.
func Test_EagerLoadFailsOnAnEntryReadError(t *testing.T) {
	const name = "eager-load-read-error"
	seedRows(t, name, 3)
	client := newCloseTestClient(t)
	defer closeClient(t, client)
	before := watchLoops()

	col := &readHookCol{
		StoreCollection: client.GetCollection("test", name),
		err:             errors.Wrap(errors.Unavailable, "server selection timed out"),
	}
	tbl := &MyTable{}
	err := tbl.Initialize(col)
	if !errors.IsUnavailable(err) || !strings.Contains(err.Error(), "row-000000") {
		t.Fatalf("Initialize = %v; want Unavailable naming the key", err)
	}
	waitWatchLoops(t, before)
	if _, err := tbl.Find(context.Background(), &MyKey{Name: "row-000000"}); !errors.IsNotFound(err) {
		t.Errorf("Find after a failed start = %v; want NotFound from an empty cache", err)
	}

	col.setErr(nil)
	if err := tbl.Initialize(col); err != nil {
		t.Fatalf("Initialize again after a failed start = %v; want success", err)
	}
	waitWatchLoops(t, before+1)
	for _, k := range []string{"row-000000", "row-000001", "row-000002"} {
		if _, err := tbl.Find(context.Background(), &MyKey{Name: k}); err != nil {
			t.Errorf("Find %s after the second start = %v; want the entry", k, err)
		}
	}
}

// An entry deleted between listing the keys and reading it reads as
// NotFound; it is left out of the cache and the start succeeds.
func Test_EagerLoadSkipsAnEntryDeletedAfterListing(t *testing.T) {
	const name = "eager-load-deleted-row"
	seedRows(t, name, 3)
	raw := rawCollection(t, name)
	client := newCloseTestClient(t)
	defer closeClient(t, client)

	var once sync.Once
	col := &readHookCol{
		StoreCollection: client.GetCollection("test", name),
		beforeRead: func(key any) {
			k, ok := key.(*MyKey)
			if !ok || k.Name != "row-000001" {
				return
			}
			once.Do(func() {
				filter := bson.D{{Key: "_id", Value: bson.D{{Key: "name", Value: k.Name}}}}
				if _, err := raw.DeleteOne(context.Background(), filter); err != nil {
					t.Errorf("delete: %s", err)
				}
			})
		},
	}
	tbl := &MyTable{}
	if err := tbl.Initialize(col); err != nil {
		t.Fatalf("Initialize = %v; want success with the deleted entry skipped", err)
	}
	if _, err := tbl.Find(context.Background(), &MyKey{Name: "row-000001"}); !errors.IsNotFound(err) {
		t.Errorf("Find of the deleted entry = %v; want NotFound", err)
	}
	for _, k := range []string{"row-000000", "row-000002"} {
		if _, err := tbl.Find(context.Background(), &MyKey{Name: k}); err != nil {
			t.Errorf("Find %s = %v; want the entry", k, err)
		}
	}
}

// A start that fails to list the keys also leaves no watch running and the
// table uninitialized, so a second Initialize on it succeeds.
func Test_EagerLoadKeyListErrorCanBeRetried(t *testing.T) {
	const name = "eager-load-list-error"
	seedRows(t, name, 2)
	client := newCloseTestClient(t)
	defer closeClient(t, client)
	before := watchLoops()

	col := &readHookCol{
		StoreCollection: client.GetCollection("test", name),
		listErr:         errors.Wrap(errors.Unavailable, "server selection timed out"),
	}
	tbl := &MyTable{}
	if err := tbl.Initialize(col); !errors.IsUnavailable(err) {
		t.Fatalf("Initialize = %v; want the key list's Unavailable", err)
	}
	waitWatchLoops(t, before)

	col.setListErr(nil)
	if err := tbl.Initialize(col); err != nil {
		t.Fatalf("Initialize again after a failed key list = %v; want success", err)
	}
	waitWatchLoops(t, before+1)
	for _, k := range []string{"row-000000", "row-000001"} {
		if _, err := tbl.Find(context.Background(), &MyKey{Name: k}); err != nil {
			t.Errorf("Find %s after the second start = %v; want the entry", k, err)
		}
	}
}

// waitCached waits until cond holds for the table's cache.
func waitCached(t *testing.T, tbl *MyTable, what string, cond func(map[MyKey]*MyData) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tbl.cacheMu.RLock()
		ok := cond(tbl.cache)
		tbl.cacheMu.RUnlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("timed out waiting for the change callback: %s", what)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// onLoadRead returns a FindOne hook that runs change once, inside the
// eager load's read of row-000001: after that read has returned the
// seeded value and before the load writes it to the cache. The first read
// of that key is the load's, since no change to it precedes the load;
// the callback's later reads of it pass straight through while change
// waits for the callback.
func onLoadRead(change func()) func(key any) {
	var first atomic.Bool
	return func(key any) {
		if k, ok := key.(*MyKey); ok && k.Name == "row-000001" && first.CompareAndSwap(false, true) {
			change()
		}
	}
}

// A change the callback applies while the load holds an older read of the
// same key is not overwritten by the load.
func Test_EagerLoadKeepsANewerValueFromTheCallback(t *testing.T) {
	const name = "eager-load-newer-value"
	seedRows(t, name, 3)
	raw := rawCollection(t, name)
	client := newCloseTestClient(t)
	defer closeClient(t, client)

	tbl := &MyTable{}
	col := &readHookCol{StoreCollection: client.GetCollection("test", name)}
	col.afterRead = onLoadRead(func() {
		filter := bson.D{{Key: "_id", Value: bson.D{{Key: "name", Value: "row-000001"}}}}
		update := bson.D{{Key: "$set", Value: bson.D{{Key: "desc", Value: "changed"}}}}
		if _, err := raw.UpdateOne(context.Background(), filter, update); err != nil {
			t.Errorf("update: %s", err)
			return
		}
		waitCached(t, tbl, "store the changed row", func(c map[MyKey]*MyData) bool {
			e, ok := c[MyKey{Name: "row-000001"}]
			return ok && e.Desc == "changed"
		})
	})
	if err := tbl.Initialize(col); err != nil {
		t.Fatalf("Initialize = %v; want success", err)
	}
	e, err := tbl.Find(context.Background(), &MyKey{Name: "row-000001"})
	if err != nil || e.Desc != "changed" {
		t.Errorf("Find of the changed row = %v, %v; want the callback's changed value", e, err)
	}
}

// A row the callback evicts while the load holds an older read of it is
// not put back by the load. The delete is followed by an insert of a row
// the load did not list; the callback handles events in order, so once
// that row is cached the eviction has been applied.
func Test_EagerLoadKeepsAnEvictionFromTheCallback(t *testing.T) {
	const name = "eager-load-evicted-row"
	seedRows(t, name, 3)
	raw := rawCollection(t, name)
	client := newCloseTestClient(t)
	defer closeClient(t, client)

	tbl := &MyTable{}
	col := &readHookCol{StoreCollection: client.GetCollection("test", name)}
	col.afterRead = onLoadRead(func() {
		filter := bson.D{{Key: "_id", Value: bson.D{{Key: "name", Value: "row-000001"}}}}
		if _, err := raw.DeleteOne(context.Background(), filter); err != nil {
			t.Errorf("delete: %s", err)
			return
		}
		marker := bson.D{{Key: "_id", Value: bson.D{{Key: "name", Value: "marker"}}}, {Key: "desc", Value: "marker"}}
		if _, err := raw.InsertOne(context.Background(), marker); err != nil {
			t.Errorf("insert: %s", err)
			return
		}
		waitCached(t, tbl, "store the marker row", func(c map[MyKey]*MyData) bool {
			_, ok := c[MyKey{Name: "marker"}]
			return ok
		})
	})
	if err := tbl.Initialize(col); err != nil {
		t.Fatalf("Initialize = %v; want success", err)
	}
	if e, err := tbl.Find(context.Background(), &MyKey{Name: "row-000001"}); !errors.IsNotFound(err) {
		t.Errorf("Find of the deleted row = %v, %v; want NotFound", e, err)
	}
}
