// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

// frames of the goroutines the reconciler starts
const (
	pipelineLoop = "reconciler.(*Pipeline).initialize("
	requeueWait  = "reconciler.(*Pipeline).initialize.func1("
	enqueueCall  = "reconciler.(*Pipeline).Enqueue("
	replayLoop   = "reconciler.(*ManagerImpl).Register.func1("
	watchLoop    = "db.(*mongoCollection).Watch.func"
)

var reconcilerFrames = []string{pipelineLoop, requeueWait, enqueueCall, replayLoop, watchLoop}

// goroutinesIn counts the goroutines whose stack contains frame.
func goroutinesIn(frame string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range bytes.Split(buf, []byte("\n\n")) {
		if strings.Contains(string(g), frame) {
			count++
		}
	}
	return count
}

func countFrames() map[string]int {
	m := map[string]int{}
	for _, f := range reconcilerFrames {
		m[f] = goroutinesIn(f)
	}
	return m
}

// waitFrames waits until the reconciler and watch goroutines are back to
// the counts in want.
func waitFrames(t *testing.T, want map[string]int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := countFrames()
		same := true
		for f, n := range want {
			if got[f] != n {
				same = false
			}
		}
		if same {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines did not end: want %v, got %v", want, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitFrame(t *testing.T, frame string, atLeast int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for goroutinesIn(frame) < atLeast {
		if time.Now().After(deadline) {
			t.Fatalf("expected at least %d goroutines in %s, found %d", atLeast, frame, goroutinesIn(frame))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newCloseTestClient(t *testing.T) db.StoreClient {
	t.Helper()
	client, err := db.NewMongoClient(&db.MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	})
	if err != nil {
		t.Fatalf("failed to connect to mongo DB Error: %s", err)
	}
	if err := client.HealthCheck(context.Background()); err != nil {
		t.Fatalf("failed to perform Health check with DB Error: %s", err)
	}
	return client
}

func closeClient(t *testing.T, client db.StoreClient) {
	t.Helper()
	if err := client.(io.Closer).Close(); err != nil {
		t.Fatalf("close failed: %s", err)
	}
}

// rawCollection returns the named collection through the driver directly,
// for seeding and for writing rows a table cannot.
func rawCollection(t *testing.T, name string) *mongo.Collection {
	t.Helper()
	c, err := mongo.Connect(options.Client().ApplyURI("mongodb://root:password@localhost:27017/?directConnection=true"))
	if err != nil {
		t.Fatalf("raw connect: %s", err)
	}
	t.Cleanup(func() { _ = c.Disconnect(context.Background()) })
	return c.Database("test").Collection(name)
}

// seedRows replaces the rows of the named collection with n rows keyed
// as MyKey.
func seedRows(t *testing.T, name string, n int) {
	t.Helper()
	col := rawCollection(t, name)
	if _, err := col.DeleteMany(context.Background(), bson.D{}); err != nil {
		t.Fatalf("clear: %s", err)
	}
	docs := make([]any, 0, n)
	for i := 0; i < n; i++ {
		docs = append(docs, bson.D{
			{Key: "_id", Value: bson.D{{Key: "name", Value: fmt.Sprintf("row-%06d", i)}}},
			{Key: "desc", Value: "seeded"},
		})
	}
	if n > 0 {
		if _, err := col.InsertMany(context.Background(), docs); err != nil {
			t.Fatalf("seed: %s", err)
		}
	}
}

// slowController reconciles every key slowly; requeueEvery-th key asks
// for a requeue an hour later and failEvery-th key fails.
type slowController struct {
	delay        time.Duration
	requeueEvery int64
	failEvery    int64
	calls        atomic.Int64
}

func (c *slowController) Reconcile(k any) (*reconciler.Result, error) {
	n := c.calls.Add(1)
	time.Sleep(c.delay)
	if c.failEvery > 0 && n%c.failEvery == 0 {
		return nil, fmt.Errorf("failing %v", k)
	}
	if c.requeueEvery > 0 && n%c.requeueEvery == 0 {
		return &reconciler.Result{RequeueAfter: time.Hour}, nil
	}
	return &reconciler.Result{}, nil
}

// gatedController blocks every Reconcile until gate is closed.
type gatedController struct {
	gate    chan struct{}
	started atomic.Int64
}

func (c *gatedController) Reconcile(k any) (*reconciler.Result, error) {
	c.started.Add(1)
	<-c.gate
	return &reconciler.Result{}, nil
}

type registrar interface {
	Register(name string, crtl reconciler.Controller) error
}

// tableKinds builds a Table and a read-through CachedTable over a
// collection, so each case runs against both.
var tableKinds = []struct {
	name string
	init func(col db.StoreCollection) (registrar, error)
}{
	{"Table", func(col db.StoreCollection) (registrar, error) {
		tbl := &Table[MyKey, MyData]{}
		return tbl, tbl.Initialize(col)
	}},
	{"CachedTable", func(col db.StoreCollection) (registrar, error) {
		tbl := &CachedTable[MyKey, MyData]{}
		return tbl, tbl.InitializeWithConfig(col, WithReadThrough())
	}},
}

// Closing the client while the replay of existing rows to a newly
// registered controller is listing them, or enqueueing them into a full
// pipeline, ends the replay and the pipeline without a panic.
func Test_CloseDuringReplay(t *testing.T) {
	for _, kind := range tableKinds {
		t.Run(kind.name, func(t *testing.T) {
			name := "close-replay-" + strings.ToLower(kind.name)
			seedRows(t, name, 20000)
			before := countFrames()

			client := newCloseTestClient(t)
			tbl, err := kind.init(client.GetCollection("test", name))
			if err != nil {
				t.Fatalf("initialize: %s", err)
			}
			crtl := &slowController{delay: 10 * time.Millisecond}
			if err := tbl.Register("slow", crtl); err != nil {
				t.Fatalf("register: %s", err)
			}
			closeClient(t, client)

			waitFrames(t, before)
			// a panic in the replay goroutine would end this test binary
			time.Sleep(3 * time.Second)
		})
	}
}

// Closing the client with a backlog in the pipeline and requeues waiting
// for their delay ends the pipeline, the replay and the requeues without
// a panic.
func Test_CloseWithBacklogAndRequeues(t *testing.T) {
	for _, kind := range tableKinds {
		t.Run(kind.name, func(t *testing.T) {
			name := "close-backlog-" + strings.ToLower(kind.name)
			seedRows(t, name, 3000)
			before := countFrames()

			client := newCloseTestClient(t)
			tbl, err := kind.init(client.GetCollection("test", name))
			if err != nil {
				t.Fatalf("initialize: %s", err)
			}
			crtl := &slowController{delay: time.Millisecond, requeueEvery: 2, failEvery: 7}
			if err := tbl.Register("backlog", crtl); err != nil {
				t.Fatalf("register: %s", err)
			}
			// the replay has filled the pipeline and waits for room,
			// and requeues are waiting; the requeue of a failed key
			// into a full pipeline may also be waiting
			waitFrame(t, enqueueCall, before[enqueueCall]+1)
			waitFrame(t, requeueWait, before[requeueWait]+3)

			closeClient(t, client)
			waitFrames(t, before)
			time.Sleep(3 * time.Second)
		})
	}
}

// Closing the client while change callbacks wait for room in a full
// pipeline ends them without a panic, and no entry is reconciled after
// the close.
func Test_CloseDuringChangeCallback(t *testing.T) {
	for _, kind := range tableKinds {
		t.Run(kind.name, func(t *testing.T) {
			name := "close-callback-" + strings.ToLower(kind.name)
			seedRows(t, name, 0)
			before := countFrames()

			client := newCloseTestClient(t)
			tbl, err := kind.init(client.GetCollection("test", name))
			if err != nil {
				t.Fatalf("initialize: %s", err)
			}
			crtl := &gatedController{gate: make(chan struct{})}
			if err := tbl.Register("gated", crtl); err != nil {
				t.Fatalf("register: %s", err)
			}
			waitFrame(t, watchLoop, before[watchLoop]+1)

			// the first change is taken by the blocked Reconcile, the
			// next 1024 fill the pipeline, and the callback for the one
			// after waits in Enqueue
			seedRows(t, name, 1100)
			waitFrame(t, enqueueCall, before[enqueueCall]+1)

			closeClient(t, client)
			started := crtl.started.Load()
			close(crtl.gate)
			waitFrames(t, before)
			time.Sleep(3 * time.Second)
			if got := crtl.started.Load(); got != started {
				t.Fatalf("%d entries reconciled after the client closed", got-started)
			}
		})
	}
}

// Register on a table whose client is closed fails.
func Test_RegisterAfterClose(t *testing.T) {
	for _, kind := range tableKinds {
		t.Run(kind.name, func(t *testing.T) {
			client := newCloseTestClient(t)
			tbl, err := kind.init(client.GetCollection("test", "close-register-"+strings.ToLower(kind.name)))
			if err != nil {
				t.Fatalf("initialize: %s", err)
			}
			closeClient(t, client)
			if err := tbl.Register("late", &slowController{}); !errors.IsFailedPrecondition(err) {
				t.Fatalf("expected FailedPrecondition, got %v", err)
			}
		})
	}
}

// A collection that does not report its client's lifetime leaves the
// reconciler on context.Background, and it still reconciles every row.
func Test_CollectionWithoutLifetime(t *testing.T) {
	type plainCollection struct{ db.StoreCollection }

	name := "close-plain-collection"
	seedRows(t, name, 50)
	client := newCloseTestClient(t)
	defer closeClient(t, client)

	col := plainCollection{client.GetCollection("test", name)}
	if _, ok := any(col).(db.ClientLifetime); ok {
		t.Fatalf("the fake collection must not implement db.ClientLifetime")
	}
	if ctx := clientLifetime(col); ctx != context.Background() {
		t.Fatalf("expected context.Background for a collection without a lifetime")
	}

	tbl := &Table[MyKey, MyData]{}
	if err := tbl.Initialize(col); err != nil {
		t.Fatalf("initialize: %s", err)
	}
	crtl := &slowController{}
	if err := tbl.Register("plain", crtl); err != nil {
		t.Fatalf("register: %s", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for crtl.calls.Load() < 50 {
		if time.Now().After(deadline) {
			t.Fatalf("reconciled %d of 50 rows", crtl.calls.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A collection obtained from a client reports a lifetime that ends when
// the client is closed.
func Test_CollectionLifetimeEndsOnClose(t *testing.T) {
	client := newCloseTestClient(t)
	ctx := clientLifetime(client.GetCollection("test", "close-lifetime"))
	if ctx == context.Background() || ctx.Err() != nil {
		t.Fatalf("expected a live client lifetime")
	}
	closeClient(t, client)
	if ctx.Err() == nil {
		t.Fatalf("client lifetime did not end on close")
	}
}

const listingChildEnv = "TABLE_LISTING_CHILD"

// While the client is open, a replay listing that fails panics as it did
// before the listing could end with the client.
func Test_ReplayListingErrorStillPanics(t *testing.T) {
	if mode := os.Getenv(listingChildEnv); mode != "" {
		runListingChild(t, mode)
		return
	}
	for _, kind := range tableKinds {
		t.Run(kind.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^Test_ReplayListingErrorStillPanics$", "-test.count=1")
			cmd.Env = append(os.Environ(), listingChildEnv+"="+kind.name)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("child exited cleanly, expected a panic; output:\n%s", out)
			}
			if !strings.Contains(string(out), "got error while fetching all keys") {
				t.Fatalf("child failed without the listing panic: %s; output:\n%s", err, out)
			}
		})
	}
}

func runListingChild(t *testing.T, mode string) {
	name := "close-listing-error-" + strings.ToLower(mode)
	// a row whose _id cannot be decoded into the key fails the listing
	raw := rawCollection(t, name)
	if _, err := raw.DeleteMany(context.Background(), bson.D{}); err != nil {
		t.Fatalf("clear: %s", err)
	}
	if _, err := raw.InsertOne(context.Background(), bson.D{{Key: "_id", Value: "not-a-document"}}); err != nil {
		t.Fatalf("insert: %s", err)
	}
	client := newCloseTestClient(t)
	for _, kind := range tableKinds {
		if kind.name != mode {
			continue
		}
		tbl, err := kind.init(client.GetCollection("test", name))
		if err != nil {
			t.Fatalf("initialize: %s", err)
		}
		if err := tbl.Register("child", &slowController{}); err != nil {
			t.Fatalf("register: %s", err)
		}
	}
	// the replay goroutine is expected to end the process before this
	time.Sleep(10 * time.Second)
	t.Fatalf("no panic")
}
