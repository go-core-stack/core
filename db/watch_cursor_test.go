// Copyright © 2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// changeStreamCursors returns the change stream cursors the server holds
// open on a database, whether a read is in progress on them or not.
func changeStreamCursors(t *testing.T, client StoreClient, dbName string) []bson.M {
	t.Helper()
	admin := client.(*mongoClient).client.Database("admin")
	cursor, err := admin.Aggregate(context.Background(), bson.A{
		bson.D{{Key: "$currentOp", Value: bson.D{
			{Key: "allUsers", Value: true},
			{Key: "idleCursors", Value: true},
		}}},
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "cursor.originatingCommand.$db", Value: dbName},
			{Key: "cursor.originatingCommand.pipeline.0.$changeStream", Value: bson.D{{Key: "$exists", Value: true}}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$cursor.cursorId"},
			{Key: "coll", Value: bson.D{{Key: "$first", Value: "$cursor.originatingCommand.aggregate"}}},
		}}},
	})
	if err != nil {
		t.Fatalf("failed to read current operations: %s", err)
	}
	var ids []bson.M
	if err := cursor.All(context.Background(), &ids); err != nil {
		t.Fatalf("failed to decode current operations: %s", err)
	}
	return ids
}

func waitChangeStreamCursors(t *testing.T, client StoreClient, dbName string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := len(changeStreamCursors(t, client, dbName))
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d change stream cursors on %s, found %d", want, dbName, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// clearCursors kills the change stream cursors an earlier run left on the
// test's database, so the test counts only its own.
func clearCursors(t *testing.T, client StoreClient, dbName string) {
	t.Helper()
	db := client.(*mongoClient).client.Database(dbName)
	for _, c := range changeStreamCursors(t, client, dbName) {
		coll, _ := c["coll"].(string)
		_ = db.RunCommand(context.Background(), bson.D{
			{Key: "killCursors", Value: coll},
			{Key: "cursors", Value: bson.A{c["_id"]}},
		}).Err()
	}
	waitChangeStreamCursors(t, client, dbName, 0)
}

// A watch or event logger that ends while the server holds its read
// leaves no cursor on the server, whether its context is cancelled or its
// client is closed.
func Test_EndedStreamLeavesNoCursor(t *testing.T) {
	const dbName = "watch-cursor"
	observer := newWatchTestClient(t)
	t.Cleanup(func() { _ = observer.(io.Closer).Close() })
	clearCursors(t, observer, dbName)

	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	col := client.GetCollection(dbName, "watched")
	if err := col.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := col.Watch(ctx, nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	logCtx, logCancel := context.WithCancel(context.Background())
	if err := col.startEventLogger(logCtx, reflect.TypeOf(watchCloseEvent{}), nil); err != nil {
		t.Fatalf("failed to start event logger: %s", err)
	}
	waitChangeStreamCursors(t, observer, dbName, 2)
	awaitedRead(t, client, dbName, "watched")

	cancel()
	waitChangeStreamCursors(t, observer, dbName, 1)
	logCancel()
	waitChangeStreamCursors(t, observer, dbName, 0)

	// and when the client is closed while they are open
	if err := col.Watch(context.Background(), nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	if err := col.startEventLogger(context.Background(), reflect.TypeOf(watchCloseEvent{}), nil); err != nil {
		t.Fatalf("failed to start event logger: %s", err)
	}
	waitChangeStreamCursors(t, observer, dbName, 2)
	awaitedRead(t, client, dbName, "watched")
	if err := client.(io.Closer).Close(); err != nil {
		t.Fatalf("close failed: %s", err)
	}
	// a cursor killed during its read goes once the server ends that read,
	// not when its session expires
	waitChangeStreamCursors(t, observer, dbName, 0)
}

// A stream the driver resumed on a new cursor, with no change since,
// still leaves no cursor on the server once it ends.
func Test_ResumedStreamLeavesNoCursor(t *testing.T) {
	const dbName = "watch-cursor-resume"
	observer := newWatchTestClient(t)
	t.Cleanup(func() { _ = observer.(io.Closer).Close() })
	clearCursors(t, observer, dbName)

	client, err := NewMongoClient(&MongoConfig{
		Host:         "localhost",
		Port:         "27017",
		Username:     "root",
		Password:     "password",
		MaxAwaitTime: time.Second,
	})
	if err != nil {
		t.Fatalf("failed to connect to mongo DB Error: %s", err)
	}
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	col := client.GetCollection(dbName, "watched")
	if err := col.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}

	// the callback holds the loop on the first change, so no read is in
	// progress when the cursor is killed
	// buffered, so the first change is held even if it arrives before
	// the test waits for it
	held := make(chan struct{}, 1)
	release := make(chan struct{})
	// released however the test ends, so a failure does not leave the
	// watch held for the tests after it
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	var once sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := col.Watch(ctx, nil, func(string, any) {
		once.Do(func() {
			held <- struct{}{}
			<-release
		})
	}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	waitChangeStreamCursors(t, observer, dbName, 1)
	key := &MyKey{Name: "resume"}
	_ = col.DeleteOne(context.Background(), key)
	if err := col.InsertOne(context.Background(), key, &MyData{Desc: "resume"}); err != nil {
		t.Fatalf("failed to insert: %s", err)
	}
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatalf("no change reached the callback")
	}

	// a cursor the server lost: the driver resumes on a new one
	first := changeStreamCursors(t, observer, dbName)
	clearCursors(t, observer, dbName)
	releaseOnce()
	waitChangeStreamCursors(t, observer, dbName, 1)
	if second := changeStreamCursors(t, observer, dbName); second[0]["_id"] == first[0]["_id"] {
		t.Fatalf("the stream did not move to a new cursor")
	}

	// one read on the new cursor returns within the max await time
	time.Sleep(1500 * time.Millisecond)
	cancel()
	waitChangeStreamCursors(t, observer, dbName, 0)
}

// A client operation timeout applies to each read, so it does not end a
// stream that stays idle for longer: the stream ending would end this
// test binary.
func Test_IdleStreamOutlivesOperationTimeout(t *testing.T) {
	client, err := NewMongoClient(&MongoConfig{
		Uri:          "mongodb://localhost:27017/?timeoutMS=2000",
		Username:     "root",
		Password:     "password",
		MaxAwaitTime: time.Second,
	})
	if err != nil {
		t.Fatalf("failed to connect to mongo DB Error: %s", err)
	}
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	if err := client.GetCollection("watch-cursor-timeout", "watched").Watch(context.Background(), nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	time.Sleep(5 * time.Second)
}
