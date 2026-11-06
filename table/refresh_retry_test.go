// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
)

// retriers counts the goroutines currently running a cached table's
// refresh retrier, recognised by their function in the stack.
func retriers() int {
	return goroutinesIn("retryRefreshes(")
}

func waitRetriers(t *testing.T, want int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d retriers", want), func() bool { return retriers() == want })
}

// isRetry reports whether a read is the retrier's: only its reads carry
// a deadline, while the callback and the eager load read under
// context.Background. A retry read without one is therefore taken for an
// event read.
func isRetry(ctx context.Context) bool {
	_, ok := ctx.Deadline()
	return ok
}

func keyName(key any) string {
	if k, ok := key.(*MyKey); ok {
		return k.Name
	}
	return ""
}

// waitFor waits until cond holds, failing the test after ten seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitClosed waits for ch to be closed, failing the test after ten
// seconds.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// untilClosed holds a read until ch is closed, or until the read's
// context ends, so a read held by a failed test still ends with the
// client.
func untilClosed(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// holdsFor fails the test if cond stops holding within d.
func holdsFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("no longer holds: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// shortRetryBounds replaces the retrier's bounds for the tables this test
// initializes, which take a copy when they start.
func shortRetryBounds(t *testing.T, b retryBounds) {
	saved := refreshRetry
	refreshRetry = b
	t.Cleanup(func() { refreshRetry = saved })
}

// logLines captures the standard logger's output until the test ends.
type logLines struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	onWrite func(line string)
}

// Write runs onWrite, if set, after storing each line, on the goroutine
// that logged it and before its log call returns.
func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	n, err := l.buf.Write(p)
	on := l.onWrite
	l.mu.Unlock()
	if on != nil {
		on(string(p))
	}
	return n, err
}

func (l *logLines) setOnWrite(fn func(line string)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onWrite = fn
}

// matching returns the captured lines that contain every one of parts.
func (l *logLines) matching(parts ...string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.buf.String(), "\n") {
		ok := line != ""
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			out = append(out, line)
		}
	}
	return out
}

func captureLog(t *testing.T) *logLines {
	l := &logLines{}
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return l
}

const (
	pendingLine = "on change event, retaining cached value and retrying"
	failedRound = "pending entries, retrying in"
	settledLine = "entries whose read had failed on a change event"
)

// retryFixture is a cached table over a readHookCol on its own client and
// collection, with the rows the test seeded; closing the client at the
// end of the test must end its retrier.
type retryFixture struct {
	tbl    *MyTable
	col    *readHookCol
	raw    *mongo.Collection
	client db.StoreClient
	closed bool
	before int
}

func newRetryFixture(t *testing.T, name string, rows int, b retryBounds) *retryFixture {
	t.Helper()
	seedRows(t, name, rows)
	shortRetryBounds(t, b)
	f := &retryFixture{raw: rawCollection(t, name), client: newCloseTestClient(t)}
	f.before = retriers()
	f.col = &readHookCol{StoreCollection: f.client.GetCollection("test", name)}
	f.tbl = &MyTable{}
	t.Cleanup(f.close(t))
	return f
}

func (f *retryFixture) init(t *testing.T, opts ...CachedTableOption) {
	t.Helper()
	if err := f.tbl.InitializeWithConfig(f.col, opts...); err != nil {
		t.Fatalf("Initialize = %v; want success", err)
	}
	waitRetriers(t, f.before+1)
}

func (f *retryFixture) close(t *testing.T) func() {
	return func() {
		if f.closed {
			return
		}
		f.closed = true
		closeClient(t, f.client)
		waitRetriers(t, f.before)
	}
}

func (f *retryFixture) setDesc(t *testing.T, name, desc string) {
	t.Helper()
	filter := bson.D{{Key: "_id", Value: bson.D{{Key: "name", Value: name}}}}
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "desc", Value: desc}}}}
	if _, err := f.raw.UpdateOne(context.Background(), filter, update); err != nil {
		t.Fatalf("update %s: %s", name, err)
	}
}

func (f *retryFixture) cached(name string) (string, bool) {
	f.tbl.cacheMu.RLock()
	defer f.tbl.cacheMu.RUnlock()
	e, ok := f.tbl.cache[MyKey{Name: name}]
	if !ok {
		return "", false
	}
	return e.Desc, true
}

func (f *retryFixture) hasDesc(name, desc string) func() bool {
	return func() bool {
		d, ok := f.cached(name)
		return ok && d == desc
	}
}

func (f *retryFixture) pending() int {
	return f.tbl.pendingCount()
}

func (f *retryFixture) isPending(name string) bool {
	_, ok := f.tbl.pendingGeneration(MyKey{Name: name})
	return ok
}

// counter counts reads by key.
type counter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *counter) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[name]++
}

func (c *counter) get(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[name]
}

// findController records, for each key it reconciles, the value Find
// returns for it.
type findController struct {
	tbl  *MyTable
	mu   sync.Mutex
	seen map[string][]string
}

func (c *findController) Reconcile(k any) (*reconciler.Result, error) {
	key := k.(*MyKey)
	desc := "<missing>"
	if e, err := c.tbl.Find(context.Background(), key); err == nil {
		desc = e.Desc
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string][]string{}
	}
	c.seen[key.Name] = append(c.seen[key.Name], desc)
	return &reconciler.Result{}, nil
}

func (c *findController) runs(name string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen[name]...)
}

// A change event whose read fails, as Unavailable or as Unknown, keeps the
// cached value and notifies the reconciler, which runs on it; the retrier
// then reads the row, the cache holds the new value, and the reconciler is
// notified again and runs on that.
func Test_RetryRefreshesAfterAFailedRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Unavailable", errors.Wrap(errors.Unavailable, "server selection timed out")},
		{"Unknown", errors.Wrap(errors.Unknown, "not primary")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t, "retry-refresh-"+strings.ToLower(tc.name), 2,
				retryBounds{minDelay: 5 * time.Millisecond, maxDelay: 40 * time.Millisecond, readTimeout: time.Second})
			f.init(t)
			ctrl := &findController{tbl: f.tbl}
			if err := f.tbl.Register("find", ctrl); err != nil {
				t.Fatalf("Register: %s", err)
			}
			waitFor(t, "the replay of row-000001", func() bool { return len(ctrl.runs("row-000001")) == 1 })

			// the event's read fails; the retrier's waits until the
			// reconciler has run on the retained value
			gate := make(chan struct{})
			f.col.setAround(func(ctx context.Context, key any, read func() error) error {
				if keyName(key) != "row-000001" {
					return read()
				}
				if !isRetry(ctx) {
					return tc.err
				}
				if err := untilClosed(ctx, gate); err != nil {
					return err
				}
				return read()
			})
			f.setDesc(t, "row-000001", "changed")
			waitFor(t, "the failed event's reconcile", func() bool { return len(ctrl.runs("row-000001")) == 2 })
			if runs := ctrl.runs("row-000001"); runs[1] != "seeded" {
				t.Fatalf("reconcile after the failed event saw %q; want the retained %q", runs[1], "seeded")
			}
			if !f.isPending("row-000001") {
				t.Fatalf("row-000001 is not pending after its read failed")
			}
			close(gate)

			waitFor(t, "the retried value in the cache", f.hasDesc("row-000001", "changed"))
			waitFor(t, "a reconcile on the retried value", func() bool {
				runs := ctrl.runs("row-000001")
				return len(runs) == 3 && runs[2] == "changed"
			})
			if n := f.pending(); n != 0 {
				t.Errorf("pending = %d after the retry; want 0", n)
			}
		})
	}
}

// However many of a key's events fail, the key has one pending entry, one
// line in the log, and the table one retrier.
func Test_RetryBurstLeavesOnePendingEntry(t *testing.T) {
	f := newRetryFixture(t, "retry-burst", 2,
		retryBounds{minDelay: time.Hour, maxDelay: time.Hour, readTimeout: time.Second})
	f.init(t)
	logs := captureLog(t)

	var eventReads counter
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		if keyName(key) != "row-000001" {
			return read()
		}
		if !isRetry(ctx) {
			eventReads.add("row-000001")
		}
		return errors.Wrap(errors.Unavailable, "burst down")
	})
	for i := 0; i < 5; i++ {
		f.setDesc(t, "row-000001", fmt.Sprintf("burst-%d", i))
	}
	waitFor(t, "five failed event reads", func() bool { return eventReads.get("row-000001") == 5 })

	if n := f.pending(); n != 1 {
		t.Errorf("pending = %d after a burst for one key; want 1", n)
	}
	if n := retriers(); n != f.before+1 {
		t.Errorf("retriers = %d; want %d", n, f.before+1)
	}
	if lines := logs.matching("row-000001", pendingLine); len(lines) != 1 {
		t.Errorf("logged %d lines for the key becoming pending; want 1: %q", len(lines), lines)
	}
}

// While the retrier backs off on a key that keeps failing, the stream goes
// on delivering and other keys go on refreshing, and a key recorded during
// the wait does not cut it short.
func Test_RetryBacksOffBesideTheStream(t *testing.T) {
	f := newRetryFixture(t, "retry-beside-stream", 3,
		retryBounds{minDelay: time.Hour, maxDelay: time.Hour, readTimeout: time.Second})
	f.init(t)

	var retryReads atomic.Int64
	down := map[string]bool{"row-000001": true, "row-000002": true}
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		if isRetry(ctx) {
			retryReads.Add(1)
		}
		if down[keyName(key)] {
			return errors.Wrap(errors.Unavailable, "row down")
		}
		return read()
	})
	f.setDesc(t, "row-000001", "changed")
	waitFor(t, "the first failed retry", func() bool { return retryReads.Load() == 1 })

	// the retrier now waits an hour; the stream does not
	f.setDesc(t, "row-000000", "changed")
	waitFor(t, "another key's refresh during the backoff", f.hasDesc("row-000000", "changed"))

	// a key recorded during the wait wakes nothing
	f.setDesc(t, "row-000002", "changed")
	waitFor(t, "row-000002 pending", func() bool { return f.isPending("row-000002") })
	f.setDesc(t, "row-000000", "changed again")
	waitFor(t, "the stream after a second failed key", f.hasDesc("row-000000", "changed again"))
	holdsFor(t, 200*time.Millisecond, "one retry read during the backoff", func() bool { return retryReads.Load() == 1 })
}

// A newer event that settles a key wins over a retry already in flight,
// whether the event reads the row after the retry has or before it does.
func Test_RetryLosesToANewerSettlingEvent(t *testing.T) {
	bounds := retryBounds{minDelay: time.Hour, maxDelay: time.Hour, readTimeout: 5 * time.Second}

	// The retry has read v1 and holds it; v2 is written and its event
	// read and applied; the retry's v1 is then dropped.
	t.Run("event reads last", func(t *testing.T) {
		f := newRetryFixture(t, "retry-settled-reads-last", 2, bounds)
		f.init(t)
		var failEvents atomic.Bool
		failEvents.Store(true)
		held, release := make(chan struct{}), make(chan struct{})
		f.col.setAround(func(ctx context.Context, key any, read func() error) error {
			if keyName(key) != "row-000001" {
				return read()
			}
			if !isRetry(ctx) {
				if failEvents.Load() {
					return errors.Wrap(errors.Unavailable, "event read failed")
				}
				return read()
			}
			err := read()
			close(held)
			if err := untilClosed(ctx, release); err != nil {
				return err
			}
			return err
		})
		f.setDesc(t, "row-000001", "v1")
		waitClosed(t, held, "held")
		failEvents.Store(false)
		f.setDesc(t, "row-000001", "v2")
		waitFor(t, "the newer event's value", f.hasDesc("row-000001", "v2"))
		close(release)
		holdsFor(t, 200*time.Millisecond, "the newer event's value after the retry", f.hasDesc("row-000001", "v2"))
		if f.isPending("row-000001") {
			t.Errorf("row-000001 is still pending after an event settled it")
		}
	})

	// The retry has begun but not yet read; v2 is written and its event
	// settles the key; the retry's read then fails, and does not put the
	// settled key back.
	t.Run("event reads first", func(t *testing.T) {
		f := newRetryFixture(t, "retry-settled-reads-first", 2, bounds)
		f.init(t)
		var failEvents atomic.Bool
		failEvents.Store(true)
		held, release := make(chan struct{}), make(chan struct{})
		var retryDone atomic.Bool
		f.col.setAround(func(ctx context.Context, key any, read func() error) error {
			if keyName(key) != "row-000001" {
				return read()
			}
			if !isRetry(ctx) {
				if failEvents.Load() {
					return errors.Wrap(errors.Unavailable, "event read failed")
				}
				return read()
			}
			close(held)
			if err := untilClosed(ctx, release); err != nil {
				return err
			}
			defer retryDone.Store(true)
			return errors.Wrap(errors.Unavailable, "retry read failed")
		})
		f.setDesc(t, "row-000001", "v1")
		waitClosed(t, held, "held")
		failEvents.Store(false)
		f.setDesc(t, "row-000001", "v2")
		waitFor(t, "the newer event's value", f.hasDesc("row-000001", "v2"))
		if f.isPending("row-000001") {
			t.Fatalf("row-000001 is still pending after an event settled it")
		}
		close(release)
		waitFor(t, "the retry's failed read", retryDone.Load)
		holdsFor(t, 200*time.Millisecond, "the settled key left out of the pending set", func() bool {
			return !f.isPending("row-000001")
		})
	})
}

// A newer event whose read fails again gives the key a new generation, so
// the result of a retry already in flight is dropped and the key is read
// again; and a key settled and recorded again never matches the generation
// an older retry read under.
func Test_RetryLosesToANewerFailingEvent(t *testing.T) {
	bounds := retryBounds{minDelay: time.Hour, maxDelay: time.Hour, readTimeout: 5 * time.Second}
	for _, tc := range []struct {
		name string
		// between runs after the first retry has read v1 and before it
		// is released; it leaves the key pending again, newer than v1
		between func(t *testing.T, f *retryFixture, failEvents *atomic.Bool, eventFails *counter)
		want    string
	}{
		{"recorded again while pending", func(t *testing.T, f *retryFixture, failEvents *atomic.Bool, eventFails *counter) {
			f.setDesc(t, "row-000001", "v2")
			waitFor(t, "the second failed event read", func() bool { return eventFails.get("row-000001") == 2 })
		}, "v2"},
		{"settled and recorded again", func(t *testing.T, f *retryFixture, failEvents *atomic.Bool, eventFails *counter) {
			failEvents.Store(false)
			f.setDesc(t, "row-000001", "v2")
			waitFor(t, "the settling event's value", f.hasDesc("row-000001", "v2"))
			if f.isPending("row-000001") {
				t.Fatalf("row-000001 is still pending after an event settled it")
			}
			failEvents.Store(true)
			f.setDesc(t, "row-000001", "v3")
			waitFor(t, "row-000001 recorded again", func() bool { return f.isPending("row-000001") })
		}, "v3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t, "retry-failing-"+strings.ReplaceAll(tc.name, " ", "-"), 2, bounds)
			f.init(t)
			var failEvents atomic.Bool
			failEvents.Store(true)
			var eventFails counter
			var retries atomic.Int64
			held, release := make(chan struct{}), make(chan struct{})
			second, gate := make(chan struct{}), make(chan struct{})
			f.col.setAround(func(ctx context.Context, key any, read func() error) error {
				if keyName(key) != "row-000001" {
					return read()
				}
				if !isRetry(ctx) {
					if failEvents.Load() {
						eventFails.add("row-000001")
						return errors.Wrap(errors.Unavailable, "event read failed")
					}
					return read()
				}
				if retries.Add(1) == 1 {
					err := read()
					close(held)
					if err := untilClosed(ctx, release); err != nil {
						return err
					}
					return err
				}
				close(second)
				if err := untilClosed(ctx, gate); err != nil {
					return err
				}
				return read()
			})
			f.setDesc(t, "row-000001", "v1")
			waitClosed(t, held, "held")
			before, _ := f.cached("row-000001")
			tc.between(t, f, &failEvents, &eventFails)
			close(release)

			// the dropped retry is followed at once by another, which
			// holds until the cache has been checked
			waitClosed(t, second, "second")
			if got, _ := f.cached("row-000001"); got == "v1" {
				t.Fatalf("cache holds the older retry's v1 over a newer event (had %q before)", before)
			}
			close(gate)
			waitFor(t, "the newest value", f.hasDesc("row-000001", tc.want))
			waitFor(t, "row-000001 settled", func() bool { return !f.isPending("row-000001") })
		})
	}
}

// A retry that finds the row gone evicts the entry.
func Test_RetryNotFoundEvicts(t *testing.T) {
	f := newRetryFixture(t, "retry-not-found", 2,
		retryBounds{minDelay: 5 * time.Millisecond, maxDelay: 40 * time.Millisecond, readTimeout: time.Second})
	f.init(t)
	ctrl := &findController{tbl: f.tbl}
	if err := f.tbl.Register("find", ctrl); err != nil {
		t.Fatalf("Register: %s", err)
	}
	waitFor(t, "the replay of row-000001", func() bool { return len(ctrl.runs("row-000001")) == 1 })

	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		if keyName(key) == "row-000001" && !isRetry(ctx) {
			return errors.Wrap(errors.Unavailable, "event read failed")
		}
		return read()
	})
	filter := bson.D{{Key: "_id", Value: bson.D{{Key: "name", Value: "row-000001"}}}}
	if _, err := f.raw.DeleteOne(context.Background(), filter); err != nil {
		t.Fatalf("delete: %s", err)
	}
	waitFor(t, "the eviction", func() bool { _, ok := f.cached("row-000001"); return !ok })
	waitFor(t, "a reconcile on the eviction", func() bool {
		runs := ctrl.runs("row-000001")
		return len(runs) > 0 && runs[len(runs)-1] == "<missing>"
	})
	if f.isPending("row-000001") {
		t.Errorf("row-000001 is still pending after its retry found it gone")
	}
}

var retryDelay = regexp.MustCompile(`retrying in (\S+),`)

// loggedDelays returns the waits logged by the failed-round lines.
func loggedDelays(t *testing.T, lines []string) []time.Duration {
	t.Helper()
	var out []time.Duration
	for _, line := range lines {
		m := retryDelay.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("no delay in %q", line)
		}
		d, err := time.ParseDuration(m[1])
		if err != nil {
			t.Fatalf("delay in %q: %s", line, err)
		}
		out = append(out, d)
	}
	return out
}

// While the database stays down, each round makes one read, the wait
// doubles from the minimum to the cap, and the keys take turns, so a key
// that keeps failing does not starve the others. The wait starts again
// from the minimum after a round that settles a key, and once nothing is
// pending. The read hook logs each failed retry read, so the captured log
// holds reads and rounds in the order they happened.
func Test_RetryWhileTheDatabaseStaysDown(t *testing.T) {
	const minDelay, maxDelay = 2 * time.Millisecond, 16 * time.Millisecond
	f := newRetryFixture(t, "retry-database-down", 3,
		retryBounds{minDelay: minDelay, maxDelay: maxDelay, readTimeout: time.Second})
	f.init(t)
	logs := captureLog(t)

	const readLine = "test: failed retry read of "
	var mu sync.Mutex
	down := map[string]bool{} // "key/event" or "key/retry": reads that fail
	set := func(isDown bool, kind string, names ...string) {
		mu.Lock()
		defer mu.Unlock()
		for _, n := range names {
			down[n+"/"+kind] = isDown
		}
	}
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		name, kind := keyName(key), "event"
		if isRetry(ctx) {
			kind = "retry"
		}
		mu.Lock()
		isDown := down[name+"/"+kind]
		mu.Unlock()
		if !isDown {
			return read()
		}
		if kind == "retry" {
			log.Printf("%s%s", readLine, name)
		}
		return errors.Wrap(errors.Unavailable, "database down")
	})

	keys := []string{"row-000000", "row-000001", "row-000002"}
	set(true, "event", keys...)
	set(true, "retry", keys...)
	for _, k := range keys {
		f.setDesc(t, k, "changed")
	}
	waitFor(t, "twenty failed retries", func() bool { return len(logs.matching(readLine)) >= 20 })

	// Phase two: two keys recover and one keeps failing. The round that
	// settles them resets the wait though a key is still pending.
	set(false, "retry", "row-000001", "row-000002")
	waitFor(t, "the recovered keys", func() bool { return f.pending() == 1 })
	settledAt := func(lines []string) int {
		for i, line := range lines {
			if strings.Contains(line, settledLine) {
				return i
			}
		}
		return -1
	}
	waitFor(t, "a failed round after the settled one", func() bool {
		lines := logs.matching("")
		i := settledAt(lines)
		if i < 0 {
			return false
		}
		for _, line := range lines[i:] {
			if strings.Contains(line, failedRound) {
				return true
			}
		}
		return false
	})
	lines := logs.matching("")
	at := settledAt(lines)
	var reads, rounds []string
	for i, line := range lines[:at] {
		if j := strings.Index(line, readLine); j >= 0 {
			if i > 0 && strings.Contains(lines[i-1], readLine) {
				t.Errorf("two reads in one round: %q then %q", lines[i-1], line)
			}
			reads = append(reads, line[j+len(readLine):])
		}
		if strings.Contains(line, failedRound) {
			rounds = append(rounds, line)
		}
	}

	// the keys take turns once all three are pending: from the first
	// read of the last of them to be read, every three reads are of
	// three different keys
	first := 0
	for _, k := range keys {
		for i, r := range reads {
			if r == k {
				first = max(first, i)
				break
			}
		}
	}
	if len(reads) < first+9 {
		t.Fatalf("%d reads with the last key first read at %d; want nine more", len(reads), first)
	}
	for i := first; i+3 <= first+9; i++ {
		w := map[string]bool{reads[i]: true, reads[i+1]: true, reads[i+2]: true}
		if len(w) != 3 {
			t.Errorf("reads %d-%d were %v; want each of the three keys", i, i+2, reads[i:i+3])
			break
		}
	}

	// the wait doubles from the minimum to the cap, with jitter over
	// its upper half
	base := minDelay
	for i, d := range loggedDelays(t, rounds) {
		if d < base/2 || d > base {
			t.Errorf("wait %d = %s; want within [%s, %s]", i, d, base/2, base)
		}
		base = min(2*base, maxDelay)
	}

	// the round that settled two keys started the wait again from the
	// minimum, though row-000000 is still pending
	for _, line := range lines[at:] {
		if strings.Contains(line, failedRound) {
			if d := loggedDelays(t, []string{line})[0]; d > minDelay {
				t.Errorf("wait after a settling round = %s; want at most %s", d, minDelay)
			}
			break
		}
	}

	// Phase three: let the wait grow to the cap again, then let an
	// event settle the last key, so the set empties without a round
	// settling anything; the next failure waits from the minimum.
	n := len(logs.matching(readLine))
	waitFor(t, "the wait back at the cap", func() bool { return len(logs.matching(readLine)) >= n+5 })
	set(false, "event", "row-000000")
	f.setDesc(t, "row-000000", "settled by its event")
	waitFor(t, "the event's settled value", f.hasDesc("row-000000", "settled by its event"))
	waitFor(t, "nothing pending", func() bool { return f.pending() == 0 })
	// the retrier's wait has ended and it has found the set empty
	waitFor(t, "the retrier out of its wait", func() bool { return goroutinesIn(").backoff(") == 0 })
	mark := len(logs.matching(failedRound))
	set(true, "event", "row-000001")
	set(true, "retry", "row-000001")
	f.setDesc(t, "row-000001", "down again")
	waitFor(t, "a failed round after the set emptied", func() bool { return len(logs.matching(failedRound)) > mark })
	if d := loggedDelays(t, logs.matching(failedRound)[mark:mark+1])[0]; d > minDelay {
		t.Errorf("first wait once the set had emptied = %s; want at most %s", d, minDelay)
	}
}

// A retry read whose server never replies is cut off by its deadline, and
// the next retry reads the row.
func Test_RetryReadIsCutOffByItsDeadline(t *testing.T) {
	f := newRetryFixture(t, "retry-read-deadline", 2,
		retryBounds{minDelay: 5 * time.Millisecond, maxDelay: 40 * time.Millisecond, readTimeout: 50 * time.Millisecond})
	f.init(t)

	var retries atomic.Int64
	cutOff := make(chan error, 1)
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		if keyName(key) != "row-000001" {
			return read()
		}
		if !isRetry(ctx) {
			return errors.Wrap(errors.Unavailable, "event read failed")
		}
		if retries.Add(1) == 1 {
			<-ctx.Done()
			cutOff <- ctx.Err()
			return ctx.Err()
		}
		return read()
	})
	f.setDesc(t, "row-000001", "changed")
	select {
	case err := <-cutOff:
		if err != context.DeadlineExceeded {
			t.Fatalf("hanging read ended with %v; want its deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("hanging retry read was not cut off")
	}
	waitFor(t, "the next retry's value", f.hasDesc("row-000001", "changed"))
}

// Closing the client ends the retrier, whether it is waiting with many
// keys pending or in the middle of a read, and it leaves without a line
// per key, or any other line.
func Test_RetryEndsWithTheClient(t *testing.T) {
	t.Run("in backoff with many keys pending", func(t *testing.T) {
		const rows = 50
		f := newRetryFixture(t, "retry-close-backoff", rows,
			retryBounds{minDelay: time.Hour, maxDelay: time.Hour, readTimeout: time.Second})
		f.init(t)
		logs := captureLog(t)

		var retryReads atomic.Int64
		f.col.setAround(func(ctx context.Context, key any, read func() error) error {
			if isRetry(ctx) {
				retryReads.Add(1)
			}
			return errors.Wrap(errors.Unavailable, "database down")
		})
		if _, err := f.raw.UpdateMany(context.Background(), bson.D{},
			bson.D{{Key: "$set", Value: bson.D{{Key: "desc", Value: "changed"}}}}); err != nil {
			t.Fatalf("update: %s", err)
		}
		waitFor(t, "every key pending", func() bool { return f.pending() == rows })
		waitFor(t, "the retrier in its wait", func() bool { return retryReads.Load() >= 1 })
		mark := len(logs.matching(""))

		f.close(t)()
		if after := logs.matching("")[mark:]; len(after) != 0 {
			t.Errorf("logged after the client closed: %q", after)
		}
	})

	t.Run("during a read", func(t *testing.T) {
		f := newRetryFixture(t, "retry-close-read", 2,
			retryBounds{minDelay: time.Millisecond, maxDelay: time.Millisecond, readTimeout: time.Hour})
		f.init(t)
		logs := captureLog(t)

		inRead := make(chan struct{})
		var once sync.Once
		f.col.setAround(func(ctx context.Context, key any, read func() error) error {
			if keyName(key) != "row-000001" {
				return read()
			}
			if !isRetry(ctx) {
				return errors.Wrap(errors.Unavailable, "event read failed")
			}
			once.Do(func() { close(inRead) })
			<-ctx.Done()
			return errors.Wrap(errors.Unknown, ctx.Err().Error())
		})
		f.setDesc(t, "row-000001", "changed")
		waitClosed(t, inRead, "inRead")
		mark := len(logs.matching(""))

		f.close(t)()
		if after := logs.matching("")[mark:]; len(after) != 0 {
			t.Errorf("logged after the client closed: %q", after)
		}
	})
}

// In read-through mode a key that is not cached is not recorded, since the
// next Find reads the database anyway; a cached key is.
func Test_RetryReadThroughSkipsUncachedKeys(t *testing.T) {
	f := newRetryFixture(t, "retry-read-through", 3,
		retryBounds{minDelay: time.Hour, maxDelay: time.Hour, readTimeout: time.Second})
	f.init(t, WithReadThrough())
	if _, err := f.tbl.Find(context.Background(), &MyKey{Name: "row-000002"}); err != nil {
		t.Fatalf("Find row-000002: %s", err)
	}

	var eventFails counter
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		if !isRetry(ctx) {
			eventFails.add(keyName(key))
		}
		return errors.Wrap(errors.Unavailable, "database down")
	})
	f.setDesc(t, "row-000001", "changed")
	f.setDesc(t, "row-000002", "changed")
	waitFor(t, "both failed event reads", func() bool {
		return eventFails.get("row-000001") == 1 && eventFails.get("row-000002") == 1
	})
	if f.isPending("row-000001") {
		t.Errorf("uncached row-000001 was recorded as pending")
	}
	if !f.isPending("row-000002") {
		t.Errorf("cached row-000002 was not recorded as pending")
	}
}

// A key whose event read fails during the eager load is retried once the
// table has started.
func Test_RetryAfterTheEagerLoad(t *testing.T) {
	f := newRetryFixture(t, "retry-eager-load", 3,
		retryBounds{minDelay: 5 * time.Millisecond, maxDelay: 40 * time.Millisecond, readTimeout: time.Second})

	var failEvents atomic.Bool
	var eventFails counter
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		if keyName(key) == "row-000001" && failEvents.Load() && !isRetry(ctx) {
			eventFails.add("row-000001")
			return errors.Wrap(errors.Unavailable, "event read failed")
		}
		return read()
	})
	// inside the load's read of row-000001, after it has read the
	// seeded value: change the row and wait for its event read to fail
	f.col.afterRead = onLoadRead(func() {
		failEvents.Store(true)
		f.setDesc(t, "row-000001", "changed")
		waitFor(t, "the failed event read", func() bool { return f.isPending("row-000001") })
	})
	f.init(t)
	waitFor(t, "the retried value", f.hasDesc("row-000001", "changed"))
	if f.isPending("row-000001") {
		t.Errorf("row-000001 is still pending after its retry")
	}
}

// An initialization that fails, in the eager load or in the manager start
// after it, discards the keys recorded during the load and starts no
// retrier; initializing again after a failed load starts exactly one.
func Test_RetryFailedStartDiscardsPending(t *testing.T) {
	for _, tc := range []struct {
		name string
		// fail makes the start fail after row-000001 has been recorded
		fail func(t *testing.T, f *retryFixture, failLoad *atomic.Bool)
		// again initializes the table a second time; the manager
		// start cannot be undone, so only a failed load is retried
		again bool
	}{
		{"in the load", func(t *testing.T, f *retryFixture, failLoad *atomic.Bool) {
			failLoad.Store(true)
		}, true},
		{"in the manager start", func(t *testing.T, f *retryFixture, failLoad *atomic.Bool) {
			if err := f.tbl.ManagerImpl.Initialize(context.Background(), f.tbl); err != nil {
				t.Fatalf("manager Initialize: %s", err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetryFixture(t, "retry-failed-start-"+strings.ReplaceAll(tc.name, " ", "-"), 3,
				retryBounds{minDelay: 5 * time.Millisecond, maxDelay: 40 * time.Millisecond, readTimeout: time.Second})
			var failEvents, failLoad atomic.Bool
			f.col.setAround(func(ctx context.Context, key any, read func() error) error {
				name := keyName(key)
				if name == "row-000001" && failEvents.Load() && !isRetry(ctx) {
					return errors.Wrap(errors.Unavailable, "event read failed")
				}
				if name == "row-000002" && failLoad.Load() {
					return errors.Wrap(errors.Unavailable, "load read failed")
				}
				return read()
			})
			f.col.afterRead = onLoadRead(func() {
				failEvents.Store(true)
				f.setDesc(t, "row-000001", "changed")
				waitFor(t, "the failed event read", func() bool { return f.isPending("row-000001") })
			})
			tc.fail(t, f, &failLoad)
			if err := f.tbl.Initialize(f.col); err == nil {
				t.Fatalf("Initialize succeeded; want it to fail")
			}
			if n := f.pending(); n != 0 {
				t.Errorf("pending = %d after a failed start; want 0", n)
			}
			holdsFor(t, 100*time.Millisecond, "no retrier after a failed start", func() bool {
				return retriers() == f.before
			})
			if !tc.again {
				return
			}
			// a second start succeeds, with exactly one retrier
			failEvents.Store(false)
			failLoad.Store(false)
			f.init(t)
			holdsFor(t, 100*time.Millisecond, "one retrier after the second start", func() bool {
				return retriers() == f.before+1
			})
		})
	}
}

// A key recorded while the retrier waits on a key that keeps failing is
// read first in the next round, ahead of the key that failed.
func Test_RetryReadsANewKeyInTheNextRound(t *testing.T) {
	f := newRetryFixture(t, "retry-new-key-first", 2,
		retryBounds{minDelay: 10 * time.Millisecond, maxDelay: 10 * time.Millisecond, readTimeout: time.Second})
	f.init(t)
	logs := captureLog(t)

	const readLine = "test: failed retry read of "
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		if isRetry(ctx) {
			log.Printf("%s%s", readLine, keyName(key))
		}
		return errors.Wrap(errors.Unavailable, "database down")
	})
	// row-000001 is recorded inside the logging of the first failed
	// round, so after row-000000 has failed and been requeued and
	// before the wait
	var once sync.Once
	logs.setOnWrite(func(line string) {
		if !strings.Contains(line, failedRound) {
			return
		}
		once.Do(func() {
			f.setDesc(t, "row-000001", "changed")
			waitFor(t, "row-000001 pending", func() bool { return f.isPending("row-000001") })
		})
	})
	f.setDesc(t, "row-000000", "changed")
	waitFor(t, "a retry read after row-000001 was recorded", func() bool {
		return len(logs.matching(readLine)) >= 2
	})
	reads := logs.matching(readLine)
	if !strings.HasSuffix(reads[0], "row-000000") || !strings.HasSuffix(reads[1], "row-000001") {
		t.Errorf("retry reads %q; want row-000000 and then the new row-000001", reads[:2])
	}
}

// A key recorded once the set has emptied during a wait is read at once,
// not after the wait: the retrier is idle.
func Test_RetryWakesOnceTheSetEmptiesDuringAWait(t *testing.T) {
	f := newRetryFixture(t, "retry-wake-after-empty", 2,
		retryBounds{minDelay: time.Hour, maxDelay: time.Hour, readTimeout: time.Second})
	f.init(t)

	var settleRow0 atomic.Bool
	var retried counter
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		name := keyName(key)
		if isRetry(ctx) {
			retried.add(name)
		} else if name == "row-000000" && settleRow0.Load() {
			return read()
		}
		return errors.Wrap(errors.Unavailable, "database down")
	})
	f.setDesc(t, "row-000000", "changed")
	waitFor(t, "the first failed retry", func() bool { return retried.get("row-000000") == 1 })
	waitFor(t, "the retrier in its hour-long wait", func() bool { return goroutinesIn(").backoff(") == 1 })

	settleRow0.Store(true)
	f.setDesc(t, "row-000000", "settled by its event")
	waitFor(t, "nothing pending", func() bool { return f.pending() == 0 })
	f.setDesc(t, "row-000001", "changed")
	waitFor(t, "the new key read during the wait", func() bool { return retried.get("row-000001") == 1 })
}

// A round after the set has emptied during a wait waits from the
// minimum, not from the doubled delay. With a minimum of an hour, a wait
// from the minimum is drawn from [30m, 1h] and a doubled one from
// [1h, 2h], so the logged wait tells them apart whatever the timing.
func Test_RetryDelayResetsOnceTheSetEmptiesDuringAWait(t *testing.T) {
	f := newRetryFixture(t, "retry-reset-after-empty", 2,
		retryBounds{minDelay: time.Hour, maxDelay: 4 * time.Hour, readTimeout: time.Second})
	f.init(t)
	logs := captureLog(t)

	var settleRow0 atomic.Bool
	f.col.setAround(func(ctx context.Context, key any, read func() error) error {
		if keyName(key) == "row-000000" && !isRetry(ctx) && settleRow0.Load() {
			return read()
		}
		return errors.Wrap(errors.Unavailable, "database down")
	})
	f.setDesc(t, "row-000000", "changed")
	waitFor(t, "the first failed round", func() bool { return len(logs.matching(failedRound)) == 1 })
	waitFor(t, "the retrier in its wait", func() bool { return goroutinesIn(").backoff(") == 1 })

	// the set empties during the wait, and a key recorded after that
	// ends it
	settleRow0.Store(true)
	f.setDesc(t, "row-000000", "settled by its event")
	waitFor(t, "nothing pending", func() bool { return f.pending() == 0 })
	f.setDesc(t, "row-000001", "changed")
	waitFor(t, "the new key's failed round", func() bool { return len(logs.matching(failedRound)) == 2 })
	if d := loggedDelays(t, logs.matching(failedRound)[1:2])[0]; d >= time.Hour {
		t.Errorf("wait after the set emptied = %s; want one from the minimum, under 1h", d)
	}
}
