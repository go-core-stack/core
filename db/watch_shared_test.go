// Copyright © 2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/go-core-stack/core/errors"
)

func deleteStage() mongo.Pipeline {
	return mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "operationType", Value: "delete"}}}}}
}

// Watches on several collections of one database, with and without
// filters, share one stream; a filter the process cannot apply keeps its
// own; the shared stream closes with its last watch.
func Test_WatchesShareOneStream(t *testing.T) {
	const dbName = "watch-shared-count"
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	clearCursors(t, client, dbName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i, col := range []string{"a", "a", "b", "c"} {
		var filter any
		if i%2 == 1 {
			filter = deleteStage()
		}
		if err := client.GetCollection(dbName, col).Watch(ctx, filter, func(string, any) {}); err != nil {
			t.Fatalf("failed to start watch %d: %s", i, err)
		}
	}
	waitChangeStreamCursors(t, client, dbName, 1)

	ownCtx, ownCancel := context.WithCancel(context.Background())
	defer ownCancel()
	projected := mongo.Pipeline{bson.D{{Key: "$project", Value: bson.D{{Key: "documentKey", Value: 1}, {Key: "operationType", Value: 1}}}}}
	if err := client.GetCollection(dbName, "a").Watch(ownCtx, projected, func(string, any) {}); err != nil {
		t.Fatalf("failed to start own watch: %s", err)
	}
	waitChangeStreamCursors(t, client, dbName, 2)

	ownCancel()
	cancel()
	waitChangeStreamCursors(t, client, dbName, 0)
}

type sharedKey struct {
	Name string
}

type sharedSeen struct {
	mu   sync.Mutex
	keys []string
}

func (s *sharedSeen) add(op string, key any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, op+":"+key.(*sharedKey).Name)
}

func (s *sharedSeen) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.keys...)
}

// Each watch gets the changes of its collection that its filter matches,
// and no other.
func Test_SharedStreamRoutes(t *testing.T) {
	const dbName = "watch-shared-route"
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	clearCursors(t, client, dbName)

	for _, col := range []string{"a", "b"} {
		if _, err := client.GetCollection(dbName, col).DeleteMany(context.Background(), bson.D{}); err != nil && !errors.IsNotFound(err) {
			t.Fatalf("failed to clear: %s", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch := func(col string, filter any) *sharedSeen {
		c := client.GetCollection(dbName, col)
		if err := c.SetKeyType(reflect.TypeOf(&sharedKey{})); err != nil {
			t.Fatalf("failed to set key type: %s", err)
		}
		seen := &sharedSeen{}
		if err := c.Watch(ctx, filter, seen.add); err != nil {
			t.Fatalf("failed to start watch: %s", err)
		}
		return seen
	}
	allA := watch("a", nil)
	deletesA := watch("a", deleteStage())
	typedB := watch("b", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.M{"documentKey._id.name": "b-2"}}}})

	write := func(col, name string) {
		c := client.GetCollection(dbName, col)
		key := &sharedKey{Name: name}
		if err := c.InsertOne(context.Background(), key, &MyData{Desc: name}); err != nil {
			t.Fatalf("failed to insert: %s", err)
		}
		if err := c.DeleteOne(context.Background(), key); err != nil {
			t.Fatalf("failed to delete: %s", err)
		}
	}
	// a last, so a change on b routed to a watch on a would reach it
	// before the changes it waits for
	write("b", "b-1")
	write("b", "b-2")
	write("a", "a-1")

	expect := map[*sharedSeen][]string{
		allA:     {"insert:a-1", "delete:a-1"},
		deletesA: {"delete:a-1"},
		typedB:   {"insert:b-2", "delete:b-2"},
	}
	deadline := time.Now().Add(5 * time.Second)
	for seen, want := range expect {
		for {
			got := seen.get()
			if fmt.Sprint(got) == fmt.Sprint(want) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("watch received %v, expected %v", got, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// nothing more arrives for any of them
	time.Sleep(300 * time.Millisecond)
	for seen, want := range expect {
		if got := seen.get(); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("watch received %v, expected %v", got, want)
		}
	}
}

// Watches starting and ending on other collections, including writes to a
// collection whose watch has ended, leave the stream as it was: it stays
// one cursor, and a watch it serves receives every change once, in order.
func Test_SharedStreamOutlivesOtherWatches(t *testing.T) {
	const dbName = "watch-shared-churn"
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	clearCursors(t, client, dbName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	col := client.GetCollection(dbName, "writes")
	if err := col.SetKeyType(reflect.TypeOf(&sharedKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}
	if _, err := col.DeleteMany(context.Background(), bson.D{}); err != nil && !errors.IsNotFound(err) {
		t.Fatalf("failed to clear: %s", err)
	}
	seen := &sharedSeen{}
	if err := col.Watch(ctx, nil, seen.add); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	before := changeStreamCursors(t, client, dbName)
	if len(before) != 1 {
		t.Fatalf("expected one change stream cursor, found %d", len(before))
	}

	const rounds, perRound = 3, 100
	const writes = rounds * perRound
	ended := client.GetCollection(dbName, "ended")
	for i := 0; i < rounds; i++ {
		// a watch that ends, then a write to its collection, the last
		// change the stream returns, then a watch on a collection nothing
		// followed before, then writes to the followed collection
		endCtx, endCancel := context.WithCancel(context.Background())
		if err := ended.Watch(endCtx, nil, func(string, any) {}); err != nil {
			t.Fatalf("failed to start watch: %s", err)
		}
		endCancel()
		time.Sleep(50 * time.Millisecond)
		if err := ended.InsertOne(context.Background(), &sharedKey{Name: fmt.Sprintf("e-%d-%d", i, time.Now().UnixNano())}, &MyData{Desc: "e"}); err != nil {
			t.Fatalf("failed to insert: %s", err)
		}
		time.Sleep(300 * time.Millisecond)
		other := client.GetCollection(dbName, fmt.Sprintf("other-%d", i))
		if err := other.Watch(ctx, nil, func(string, any) {}); err != nil {
			t.Fatalf("failed to start watch %d: %s", i, err)
		}
		for j := i * perRound; j < (i+1)*perRound; j++ {
			key := &sharedKey{Name: fmt.Sprintf("w-%03d", j)}
			if err := col.InsertOne(context.Background(), key, &MyData{Desc: "w"}); err != nil {
				t.Fatalf("failed to insert: %s", err)
			}
		}
	}

	var want []string
	for i := 0; i < writes; i++ {
		want = append(want, fmt.Sprintf("insert:w-%03d", i))
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(seen.get()) < writes && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if got := seen.get(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("received %d changes, expected %d in order; first difference at %d", len(got), writes, firstDiff(got, want))
	}
	after := changeStreamCursors(t, client, dbName)
	if len(after) != 1 || fmt.Sprint(after[0]["_id"]) != fmt.Sprint(before[0]["_id"]) {
		t.Fatalf("the stream's cursor changed from %v to %v", before, after)
	}
}

func firstDiff(a, b []string) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	return len(a)
}

// A watch started at run time, while other watches start and end, receives
// every change made to its collection after Watch returns, in order and
// once each.
func Test_SharedStreamNewWatchMissesNothing(t *testing.T) {
	const dbName = "watch-shared-new"
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	clearCursors(t, client, dbName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// a stream already open, serving another collection
	if err := client.GetCollection(dbName, "base").Watch(ctx, nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	target := client.GetCollection(dbName, "target")
	if err := target.SetKeyType(reflect.TypeOf(&sharedKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}
	if _, err := target.DeleteMany(context.Background(), bson.D{}); err != nil && !errors.IsNotFound(err) {
		t.Fatalf("failed to clear: %s", err)
	}

	// writes to the target go on throughout; started counts the writes
	// begun, so a write numbered at or above its value when Watch returns
	// was made after
	const writes = 300
	var started atomic.Int64
	done := make(chan struct{})
	// the second half of the writes waits for Watch to return, so some
	// are always made after it
	watching := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < writes; i++ {
			if i == writes/2 {
				select {
				case <-watching:
				case <-ctx.Done():
					return
				}
			}
			started.Store(int64(i))
			key := &sharedKey{Name: fmt.Sprintf("n-%03d", i)}
			if err := target.InsertOne(context.Background(), key, &MyData{Desc: "n"}); err != nil {
				t.Errorf("failed to insert: %s", err)
				return
			}
		}
	}()
	// watches on other collections start and end around the new watch
	others := make(chan struct{})
	go func() {
		defer close(others)
		for i := 0; i < 10; i++ {
			otherCtx, otherCancel := context.WithCancel(ctx)
			other := client.GetCollection(dbName, fmt.Sprintf("other-%d", i))
			err := other.Watch(otherCtx, nil, func(string, any) {})
			time.Sleep(5 * time.Millisecond)
			otherCancel()
			if err != nil {
				t.Errorf("failed to start watch %d: %s", i, err)
				return
			}
		}
	}()
	// once the writes are under way
	ready := time.Now().Add(10 * time.Second)
	for started.Load() < 20 {
		select {
		case <-done:
			t.Fatalf("the writes ended at %d before the watch started", started.Load())
		default:
		}
		if time.Now().After(ready) {
			t.Fatalf("the writes did not get under way")
		}
		time.Sleep(time.Millisecond)
	}
	seen := &sharedSeen{}
	if err := target.Watch(ctx, nil, seen.add); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	// the write numbered started may have begun before Watch returned
	from := int(started.Load()) + 1
	close(watching)
	<-done
	<-others

	var want []string
	for i := from; i < writes; i++ {
		want = append(want, fmt.Sprintf("insert:n-%03d", i))
	}
	// until the last write arrives, then a little longer for duplicates
	last := fmt.Sprintf("insert:n-%03d", writes-1)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := seen.get(); len(got) > 0 && got[len(got)-1] == last {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	got := seen.get()
	// what arrived from before Watch returned comes first; the rest is
	// every write after it, in order and once each
	i := 0
	for i < len(got) && got[i] < fmt.Sprintf("insert:n-%03d", from) {
		i++
	}
	if fmt.Sprint(got[i:]) != fmt.Sprint(want) {
		t.Fatalf("after Watch returned at write %d, received %d changes, expected %d; first difference at %d", from, len(got[i:]), len(want), firstDiff(got[i:], want))
	}
}

// A callback that does not return delays only its own watch's changes.
func Test_SharedStreamSlowCallback(t *testing.T) {
	const dbName = "watch-shared-slow"
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	clearCursors(t, client, dbName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	col := client.GetCollection(dbName, "writes")
	if err := col.SetKeyType(reflect.TypeOf(&sharedKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}
	block := make(chan struct{})
	defer close(block)
	if err := col.Watch(ctx, nil, func(string, any) { <-block }); err != nil {
		t.Fatalf("failed to start blocked watch: %s", err)
	}
	got := make(chan string, 10)
	if err := col.Watch(ctx, nil, func(op string, _ any) { got <- op }); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	for i := 0; i < 2; i++ {
		key := &sharedKey{Name: fmt.Sprintf("slow-%d", i)}
		_ = col.DeleteOne(context.Background(), key)
		if err := col.InsertOne(context.Background(), key, &MyData{Desc: "slow"}); err != nil {
			t.Fatalf("failed to insert: %s", err)
		}
	}
	deadline := time.After(5 * time.Second)
	for inserts := 0; inserts < 2; {
		select {
		case op := <-got:
			if op == "insert" {
				inserts++
			}
		case <-deadline:
			t.Fatalf("changes did not arrive while another watch's callback was blocked")
		}
	}
}

// The matcher agrees with the server on every filter form it accepts: the
// same changes go through each filter applied by a stream of its own and
// by the matcher on a stream without one.
func Test_EventMatcherAgreesWithServer(t *testing.T) {
	const dbName = "watch-shared-match"
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })
	raw := client.(*mongoClient).client.Database(dbName).Collection("docs")
	if _, err := raw.DeleteMany(context.Background(), bson.D{}); err != nil {
		t.Fatalf("failed to clear: %s", err)
	}

	filters := []bson.D{
		{{Key: "operationType", Value: "delete"}},
		{{Key: "operationType", Value: bson.D{{Key: "$in", Value: bson.A{"insert", "update"}}}}},
		{{Key: "fullDocument.s", Value: "x"}},
		{{Key: "fullDocument.s", Value: bson.D{{Key: "$eq", Value: "x"}}}},
		{{Key: "fullDocument.b", Value: true}},
		{{Key: "fullDocument.b", Value: "true"}},
		{{Key: "fullDocument.tags", Value: "red"}},
		{{Key: "fullDocument.items.k", Value: "y"}},
		{{Key: "fullDocument.s", Value: "x"}, {Key: "fullDocument.b", Value: true}},
		{{Key: "fullDocument.s", Value: bson.D{{Key: "$in", Value: bson.A{"x", "y"}}}}},
		{{Key: "documentKey._id.type", Value: "t1"}},
		{{Key: "updateDescription.updatedFields.status", Value: bson.D{{Key: "$in", Value: bson.A{"done", "failed"}}}}},
		{{Key: "fullDocument.missing", Value: "x"}},
	}
	docs := []bson.D{
		{{Key: "_id", Value: bson.D{{Key: "type", Value: "t1"}, {Key: "n", Value: 1}}}, {Key: "s", Value: "x"}, {Key: "n", Value: int32(2)}, {Key: "b", Value: true}},
		{{Key: "_id", Value: bson.D{{Key: "type", Value: "t2"}, {Key: "n", Value: 2}}}, {Key: "s", Value: "y"}, {Key: "n", Value: int64(2)}, {Key: "b", Value: true}},
		{{Key: "_id", Value: bson.D{{Key: "type", Value: "t1"}, {Key: "n", Value: 3}}}, {Key: "n", Value: 2.0}, {Key: "tags", Value: bson.A{"blue", "red"}}},
		{{Key: "_id", Value: bson.D{{Key: "type", Value: "t3"}, {Key: "n", Value: 4}}}, {Key: "n", Value: 2.5}, {Key: "items", Value: bson.A{bson.D{{Key: "k", Value: "z"}}, bson.D{{Key: "k", Value: "y"}}}}},
		{{Key: "_id", Value: bson.D{{Key: "type", Value: "t3"}, {Key: "n", Value: 5}}}, {Key: "s", Value: bson.A{"x"}}, {Key: "b", Value: "true"}, {Key: "n", Value: "2"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		mu   sync.Mutex
		ids  []string
		done chan struct{}
	}
	// collect starts a stream with pipeline, and returns what it receives
	// as the _id of each change event, filtered by keep, until the marker
	// written last, which every stream lets through
	marker := bson.D{{Key: "_id", Value: bson.D{{Key: "type", Value: "end"}}}}
	isMarker := func(ev bson.Raw) bool {
		v, _ := ev.Lookup("documentKey", "_id", "type").StringValueOK()
		return v == "end"
	}
	collect := func(pipeline mongo.Pipeline, keep func(bson.Raw) bool) *result {
		cs, err := raw.Watch(ctx, pipeline)
		if err != nil {
			t.Fatalf("failed to start stream: %s", err)
		}
		r := &result{done: make(chan struct{})}
		go func() {
			defer func() { _ = cs.Close(context.Background()) }()
			for cs.Next(ctx) {
				if isMarker(cs.Current) {
					close(r.done)
					return
				}
				if keep(cs.Current) {
					r.mu.Lock()
					r.ids = append(r.ids, cs.Current.Lookup("documentKey").String()+cs.Current.Lookup("operationType").StringValue())
					r.mu.Unlock()
				}
			}
		}()
		return r
	}
	var server, local []*result
	for _, f := range filters {
		pipeline := mongo.Pipeline{bson.D{{Key: "$match", Value: f}}}
		matcher, ok := parseWatchFilter(pipeline)
		if !ok {
			t.Fatalf("filter %v is not accepted", f)
		}
		withMarker := mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "$or", Value: bson.A{f, bson.D{{Key: "documentKey._id.type", Value: "end"}}}}}}}}
		server = append(server, collect(withMarker, func(bson.Raw) bool { return true }))
		local = append(local, collect(nil, matcher.matches))
	}
	// an update event carries no full document by default, as with
	// Watch, so the filters on fullDocument match inserts, and the
	// updates exercise updateDescription
	for _, d := range docs {
		if _, err := raw.InsertOne(context.Background(), d); err != nil {
			t.Fatalf("failed to insert: %s", err)
		}
		if _, err := raw.UpdateOne(context.Background(), bson.D{{Key: "_id", Value: d[0].Value}}, bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "done"}}}}); err != nil {
			t.Fatalf("failed to update: %s", err)
		}
	}
	if _, err := raw.DeleteMany(context.Background(), bson.D{}); err != nil {
		t.Fatalf("failed to delete: %s", err)
	}
	if _, err := raw.InsertOne(context.Background(), marker); err != nil {
		t.Fatalf("failed to insert the marker: %s", err)
	}
	deadline := time.After(10 * time.Second)
	for _, r := range append(append([]*result{}, server...), local...) {
		select {
		case <-r.done:
		case <-deadline:
			t.Fatalf("a stream ended or stalled before the marker")
		}
	}
	for i, f := range filters {
		server[i].mu.Lock()
		local[i].mu.Lock()
		if fmt.Sprint(server[i].ids) != fmt.Sprint(local[i].ids) {
			t.Errorf("filter %v: server matched %d changes, the matcher %d:\nserver %v\nlocal  %v", f, len(server[i].ids), len(local[i].ids), server[i].ids, local[i].ids)
		}
		if i < len(filters)-1 && len(server[i].ids) == 0 {
			t.Errorf("filter %v matched nothing; the test does not exercise it", f)
		}
		server[i].mu.Unlock()
		local[i].mu.Unlock()
	}
}

// namedString stands for a caller's own string type, as a status enum.
type namedString string

func Test_ParseWatchFilter(t *testing.T) {
	cases := []struct {
		name     string
		pipeline mongo.Pipeline
		shared   bool
		filtered bool
	}{
		{"none", nil, true, false},
		{"empty match", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{}}}}, true, false},
		{"equality", deleteStage(), true, true},
		{"bson.M", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.M{"documentKey._id.type": "x"}}}}, true, true},
		{"in", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "a", Value: bson.D{{Key: "$in", Value: bson.A{"x", true}}}}}}}}, true, true},
		{"in a Go slice", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.M{"a": bson.M{"$in": []string{"x", "y"}}}}}}, true, true},
		{"named string type", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "a", Value: namedString("x")}}}}}, true, true},
		{"number", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "a", Value: 2}}}}}, false, false},
		{"number in", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "a", Value: bson.D{{Key: "$in", Value: bson.A{"x", 1}}}}}}}}, false, false},
		{"two stages", mongo.Pipeline{deleteStage()[0], deleteStage()[0]}, false, false},
		{"project", mongo.Pipeline{bson.D{{Key: "$project", Value: bson.D{{Key: "a", Value: 1}}}}}, false, false},
		{"top-level or", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "$or", Value: bson.A{}}}}}}, false, false},
		{"other operator", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "a", Value: bson.D{{Key: "$ne", Value: "x"}}}}}}}, false, false},
		{"document value", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "a", Value: bson.D{{Key: "b", Value: "x"}}}}}}}, false, false},
		{"array index", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "a.0", Value: "x"}}}}}, false, false},
		{"null", mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "a", Value: nil}}}}}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matcher, ok := parseWatchFilter(tc.pipeline)
			if ok != tc.shared {
				t.Fatalf("shared = %v, expected %v", ok, tc.shared)
			}
			if ok && (matcher != nil) != tc.filtered {
				t.Errorf("filtered = %v, expected %v", matcher != nil, tc.filtered)
			}
		})
	}
}

// Closing the client kills the shared stream's cursor on the server.
func Test_CloseKillsSharedCursor(t *testing.T) {
	const dbName = "watch-shared-close"
	observer := newWatchTestClient(t)
	t.Cleanup(func() { _ = observer.(io.Closer).Close() })
	clearCursors(t, observer, dbName)

	client := newWatchTestClient(t)
	if err := client.GetCollection(dbName, "a").Watch(context.Background(), nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	waitChangeStreamCursors(t, observer, dbName, 1)
	if err := client.(io.Closer).Close(); err != nil {
		t.Fatalf("close failed: %s", err)
	}
	if n := len(changeStreamCursors(t, observer, dbName)); n != 0 {
		t.Errorf("%d change stream cursors open after close", n)
	}
}

// A filter the caller changes after Watch returns changes nothing the
// watch was started with.
func Test_SharedFilterIsCopied(t *testing.T) {
	const dbName = "watch-shared-copy"
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	col := client.GetCollection(dbName, "kinds")
	if err := col.SetKeyType(reflect.TypeOf(&sharedKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}
	filter := bson.M{"documentKey._id.name": "a"}
	seen := &sharedSeen{}
	if err := col.Watch(ctx, mongo.Pipeline{bson.D{{Key: "$match", Value: filter}}}, seen.add); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	filter["documentKey._id.name"] = "b"
	for _, name := range []string{"a", "b"} {
		key := &sharedKey{Name: name}
		_ = col.DeleteOne(context.Background(), key)
		if err := col.InsertOne(context.Background(), key, &MyData{Desc: name}); err != nil {
			t.Fatalf("failed to insert: %s", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(seen.get()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	for _, got := range seen.get() {
		if got != "insert:a" && got != "delete:a" {
			t.Fatalf("watch on name a received %s", got)
		}
	}
	if len(seen.get()) == 0 {
		t.Fatalf("watch on name a received nothing")
	}
}

// A watch whose key type is *bson.M receives a *bson.M, as with a stream
// of its own; without a key type it receives a bson.M.
func Test_SharedKeyTypes(t *testing.T) {
	const dbName = "watch-shared-keys"
	client := newWatchTestClient(t)
	t.Cleanup(func() { _ = client.(io.Closer).Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	typed := client.GetCollection(dbName, "typed")
	if err := typed.SetKeyType(reflect.TypeOf(&bson.M{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}
	untyped := client.GetCollection(dbName, "untyped")
	got := make(chan string, 4)
	report := func(_ string, key any) {
		got <- fmt.Sprintf("%T", key)
	}
	if err := typed.Watch(ctx, nil, report); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	if err := typed.InsertOne(context.Background(), bson.M{"name": fmt.Sprint(time.Now().UnixNano())}, &MyData{Desc: "typed"}); err != nil {
		t.Fatalf("failed to insert: %s", err)
	}
	select {
	case k := <-got:
		if k != "*bson.M" {
			t.Errorf("typed watch received %s, expected *bson.M", k)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("typed watch received nothing")
	}
	if err := untyped.Watch(ctx, nil, report); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	if err := untyped.InsertOne(context.Background(), bson.M{"name": fmt.Sprint(time.Now().UnixNano())}, &MyData{Desc: "untyped"}); err != nil {
		t.Fatalf("failed to insert: %s", err)
	}
	select {
	case k := <-got:
		if k != "bson.M" {
			t.Errorf("untyped watch received %s, expected bson.M", k)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("untyped watch received nothing")
	}
}
