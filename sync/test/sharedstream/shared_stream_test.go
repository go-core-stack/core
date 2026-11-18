// Copyright © 2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Package sharedstream checks, in a test binary of its own, how many change
// streams a process holds on a database whatever it follows there: the
// sync owner is initialised once per process.
package sharedstream

import (
	"context"
	"io"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/sync"
	"github.com/go-core-stack/core/table"
)

const dbName = "shared-stream-count"

type rowKey struct {
	Name string
}

type row struct {
	Desc string
}

type lockKey struct {
	Name string
}

// cursorIDs returns the change stream cursors the server holds open on
// the database, through a driver client of its own.
func cursorIDs(t *testing.T, admin *mongo.Database) []bson.M {
	t.Helper()
	cur, err := admin.Aggregate(context.Background(), bson.A{
		bson.D{{Key: "$currentOp", Value: bson.D{{Key: "allUsers", Value: true}, {Key: "idleCursors", Value: true}}}},
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "cursor.originatingCommand.$db", Value: dbName},
			{Key: "cursor.originatingCommand.pipeline.0.$changeStream", Value: bson.D{{Key: "$exists", Value: true}}},
		}}},
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$cursor.cursorId"}}}},
	})
	if err != nil {
		t.Fatalf("failed to read current operations: %s", err)
	}
	var ids []bson.M
	if err := cur.All(context.Background(), &ids); err != nil {
		t.Fatalf("failed to decode current operations: %s", err)
	}
	return ids
}

// clearCursors kills the cursors an earlier run left, which the server
// keeps for ten minutes, so the test counts only its own.
func clearCursors(t *testing.T, admin *mongo.Database) {
	t.Helper()
	for _, id := range cursorIDs(t, admin) {
		for _, coll := range []string{"$cmd.aggregate", "owner-table"} {
			_ = admin.Client().Database(dbName).RunCommand(context.Background(), bson.D{
				{Key: "killCursors", Value: coll},
				{Key: "cursors", Value: bson.A{id["_id"]}},
			}).Err()
		}
	}
	// a cursor in a read is killed once that read ends
	deadline := time.Now().Add(15 * time.Second)
	for len(cursorIDs(t, admin)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d change stream cursors left open before the test", len(cursorIDs(t, admin)))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Two tables, three lock tables, the sync owner and a direct watch on one
// store hold one change stream between them.
func Test_OneStreamPerDatabase(t *testing.T) {
	raw, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017").SetAuth(options.Credential{
		AuthMechanism: "SCRAM-SHA-256", AuthSource: "admin", Username: "root", Password: "password",
	}))
	if err != nil {
		t.Fatalf("failed to connect: %s", err)
	}
	t.Cleanup(func() { _ = raw.Disconnect(context.Background()) })
	admin := raw.Database("admin")
	clearCursors(t, admin)

	client, err := db.NewMongoClient(&db.MongoConfig{Host: "localhost", Port: "27017", Username: "root", Password: "password"})
	if err != nil {
		t.Fatalf("failed to connect: %s", err)
	}
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	store := client.GetDataStore(dbName)

	if err := sync.InitializeOwner(context.Background(), store, "shared-stream-owner"); err != nil {
		t.Fatalf("failed to initialise the owner: %s", err)
	}
	for _, name := range []string{"rows-a", "rows-b"} {
		tbl := &table.Table[rowKey, row]{}
		if err := tbl.Initialize(store.GetCollection(name)); err != nil {
			t.Fatalf("failed to initialise table %s: %s", name, err)
		}
	}
	for _, name := range []string{"locks-a", "locks-b", "locks-c"} {
		if _, err := sync.LocateLockTable[lockKey](store, name); err != nil {
			t.Fatalf("failed to locate lock table %s: %s", name, err)
		}
	}
	if err := store.GetCollection("direct").Watch(context.Background(), nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		n := len(cursorIDs(t, admin))
		if n == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 1 change stream cursor on %s, found %d", dbName, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
