// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// openCursorCount reads the number of currently open cursors reported by the
// mongod via serverStatus().metrics.cursor.open.total. This is the server's
// own accounting of live cursors and is the ground truth for whether a change
// stream's cursor was reclaimed (killCursors issued) or orphaned on watch
// teardown. See CORE-0045.
func openCursorCount(t *testing.T, mc *mongoCollection) int64 {
	t.Helper()
	var status bson.M
	err := mc.col.Database().Client().
		Database("admin").
		RunCommand(context.Background(), bson.D{{Key: "serverStatus", Value: 1}}).
		Decode(&status)
	if err != nil {
		t.Fatalf("failed to run serverStatus: %s", err)
	}
	metrics, ok := status["metrics"].(bson.M)
	if !ok {
		t.Fatalf("serverStatus missing metrics section")
	}
	cursor, ok := metrics["cursor"].(bson.M)
	if !ok {
		t.Fatalf("serverStatus missing metrics.cursor section")
	}
	open, ok := cursor["open"].(bson.M)
	if !ok {
		t.Fatalf("serverStatus missing metrics.cursor.open section")
	}
	switch v := open["total"].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	default:
		t.Fatalf("unexpected type %T for metrics.cursor.open.total", open["total"])
		return 0
	}
}

// Test_WatchCursorLifecycle asserts CORE-0045: cancelling a Watch's context
// must reclaim the server-side change-stream cursor (via killCursors) rather
// than orphaning it. Before the fix, each completed watch leaked exactly one
// cursor because Close ran only after the stream's own context was already
// cancelled, so killCursors was never issued.
//
// The test opens several watches, cancels them, and asserts the server's open
// cursor count returns to (approximately) the pre-watch baseline. It allows a
// small slack for unrelated background cursors that mongod may open on its own
// during the measurement window; the pre-fix leak (one per watch) is far
// larger than that slack, so the assertion is robust.
func Test_WatchCursorLifecycle(t *testing.T) {
	config := &MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}

	client, err := NewMongoClient(config)
	if err != nil {
		t.Fatalf("failed to connect to mongo DB: %s", err)
	}
	if err := client.HealthCheck(context.Background()); err != nil {
		t.Fatalf("failed health check with DB: %s", err)
	}

	s := client.GetDataStore("test")
	col := s.GetCollection("watch_lifecycle")
	mc := col.(*mongoCollection)
	defer func() { _, _ = col.DeleteMany(context.Background(), bson.D{}) }()

	if err := col.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}

	const watches = 5
	// allowedSlack tolerates unrelated cursors mongod may open during the
	// window (e.g. internal maintenance). It is far below the pre-fix leak of
	// one cursor per watch, so a regression still fails clearly.
	const allowedSlack = int64(2)

	baseline := openCursorCount(t, mc)

	for i := 0; i < watches; i++ {
		watchCtx, cancel := context.WithCancel(context.Background())
		noop := func(op string, key any) {}
		if err := col.Watch(watchCtx, nil, noop); err != nil {
			cancel()
			t.Fatalf("failed to start watch %d: %s", i, err)
		}
		// let the change stream establish its server-side cursor.
		time.Sleep(200 * time.Millisecond)
		// cancel the caller context: this must trigger a killCursors teardown
		// on a live context, reclaiming the server-side cursor.
		cancel()
		// give runChangeStream time to issue killCursors and for the server to
		// account for the reclaimed cursor.
		time.Sleep(500 * time.Millisecond)
	}

	// after all watches are cancelled, the open cursor count must return to the
	// baseline (within slack). Before the fix, it would be baseline + watches.
	after := openCursorCount(t, mc)
	if after > baseline+allowedSlack {
		t.Fatalf("change-stream cursor leak detected (CORE-0045): baseline open cursors=%d, after %d cancelled watches=%d (allowed slack=%d); "+
			"cancelling a watch must reclaim its server-side cursor via killCursors",
			baseline, watches, after, allowedSlack)
	}
}
