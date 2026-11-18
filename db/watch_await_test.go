// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/errors"
)

// awaitedRead waits until the server holds a getMore open on the given
// collection, or on the database's shared stream when colName is empty,
// and returns the maxTimeMS that read was sent with. The server answers an
// awaited change stream read only when a change arrives or that time
// passes, so it is the stream's await time.
func awaitedRead(t *testing.T, client StoreClient, dbName, colName string) int64 {
	t.Helper()
	admin := client.(*mongoClient).client.Database("admin")
	match := bson.D{
		{Key: "ns", Value: dbName + "." + colName},
		{Key: "command.getMore", Value: bson.D{{Key: "$exists", Value: true}}},
	}
	if colName == "" {
		match = bson.D{
			{Key: "ns", Value: dbName + ".$cmd"},
			{Key: "command.collection", Value: "$cmd.aggregate"},
			{Key: "command.getMore", Value: bson.D{{Key: "$exists", Value: true}}},
		}
	}
	pipeline := bson.A{
		bson.D{{Key: "$currentOp", Value: bson.D{{Key: "allUsers", Value: true}}}},
		bson.D{{Key: "$match", Value: match}},
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cursor, err := admin.Aggregate(context.Background(), pipeline)
		if err != nil {
			t.Fatalf("failed to read current operations: %s", err)
		}
		var ops []struct {
			Command struct {
				MaxTimeMS int64 `bson:"maxTimeMS"`
			} `bson:"command"`
		}
		if err := cursor.All(context.Background(), &ops); err != nil {
			t.Fatalf("failed to decode current operations: %s", err)
		}
		if len(ops) > 0 {
			return ops[0].Command.MaxTimeMS
		}
		if time.Now().After(deadline) {
			t.Fatalf("no awaited read found on %s.%s", dbName, colName)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Every change stream waits defaultMaxAwaitTime on an idle read unless the
// client sets otherwise, Watch and the event logger alike.
func Test_WatchMaxAwaitTime(t *testing.T) {
	cases := []struct {
		name   string
		conf   time.Duration
		expect int64
	}{
		{"default", 0, defaultMaxAwaitTime.Milliseconds()},
		{"configured", 3 * time.Second, 3000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewMongoClient(&MongoConfig{
				Host:         "localhost",
				Port:         "27017",
				Username:     "root",
				Password:     "password",
				MaxAwaitTime: tc.conf,
			})
			if err != nil {
				t.Fatalf("failed to connect to mongo DB Error: %s", err)
			}
			t.Cleanup(func() { _ = client.(io.Closer).Close() })

			watchDB := "watch-await-" + tc.name
			col := client.GetCollection(watchDB, "watched")
			if err := col.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
				t.Fatalf("failed to set key type: %s", err)
			}
			if err := col.Watch(context.Background(), nil, func(string, any) {}); err != nil {
				t.Fatalf("failed to start watch: %s", err)
			}
			if got := awaitedRead(t, client, watchDB, ""); got != tc.expect {
				t.Errorf("watch read waits %d ms, expected %d", got, tc.expect)
			}

			logged := "watch-await-log-" + tc.name
			logCol := client.GetCollection("test", logged).(*mongoCollection)
			if err := logCol.startEventLogger(context.Background(), reflect.TypeOf(watchCloseEvent{}), nil); err != nil {
				t.Fatalf("failed to start event logger: %s", err)
			}
			if got := awaitedRead(t, client, "test", logged); got != tc.expect {
				t.Errorf("event logger read waits %d ms, expected %d", got, tc.expect)
			}
		})
	}
}

// A change reaches the callback while the read is held open, not when the
// await time passes.
func Test_WatchDeliversDuringAwait(t *testing.T) {
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })

	col := client.GetCollection("watch-await-deliver", "watched")
	if err := col.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}
	_, _ = col.DeleteMany(context.Background(), bson.D{})

	got := make(chan string, 1)
	if err := col.Watch(context.Background(), nil, func(op string, _ any) {
		select {
		case got <- op:
		default:
		}
	}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	awaitedRead(t, client, "watch-await-deliver", "")

	start := time.Now()
	if err := col.InsertOne(context.Background(), &MyKey{Name: "await"}, &MyData{Desc: "await"}); err != nil {
		t.Fatalf("failed to insert: %s", err)
	}
	select {
	case <-got:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("change delivered after %s", elapsed)
		}
	case <-time.After(defaultMaxAwaitTime - time.Second):
		t.Fatalf("change not delivered before the await time")
	}
}

// Ending a stream, by its context or by closing the client, does not wait
// for the read the server is holding open.
func Test_StreamEndsDuringAwait(t *testing.T) {
	before := streamLoops()

	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	col := client.GetCollection("watch-await-end", "watched")
	if err := col.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := col.Watch(ctx, nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	awaitedRead(t, client, "watch-await-end", "")
	start := time.Now()
	cancel()
	waitStreamLoops(t, before)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancelled watch ended after %s", elapsed)
	}

	// a database of its own, so the read found is this stream's and not
	// the cancelled one's, which the server may still hold
	closeCol := client.GetCollection("watch-await-close", "watched")
	if err := closeCol.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}
	if err := closeCol.Watch(context.Background(), nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	awaitedRead(t, client, "watch-await-close", "")
	start = time.Now()
	if err := client.(io.Closer).Close(); err != nil {
		t.Fatalf("close failed: %s", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("close took %s", elapsed)
	}
	waitStreamLoops(t, before)
}

// A client whose operation timeout is not longer than the change stream
// wait is refused, since the driver would refuse every stream it starts.
func Test_ClientTimeoutAgainstMaxAwaitTime(t *testing.T) {
	cases := []struct {
		name    string
		uri     string
		await   time.Duration
		refused bool
	}{
		{"no timeout", "mongodb://localhost:27017", 0, false},
		{"shorter than the default wait", "mongodb://localhost:27017/?timeoutMS=5000", 0, true},
		{"equal to the default wait", "mongodb://localhost:27017/?timeoutMS=10000", 0, true},
		{"longer than the default wait", "mongodb://localhost:27017/?timeoutMS=20000", 0, false},
		{"longer than a configured wait", "mongodb://localhost:27017/?timeoutMS=5000", 2 * time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewMongoClient(&MongoConfig{
				Uri:          tc.uri,
				Username:     "root",
				Password:     "password",
				MaxAwaitTime: tc.await,
			})
			if tc.refused {
				if !errors.IsInvalidArgument(err) {
					t.Errorf("expected InvalidArgument, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected a client, got %v", err)
			}
			_ = client.(io.Closer).Close()
		})
	}
}
