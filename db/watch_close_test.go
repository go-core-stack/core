// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/errors"
)

func newWatchTestClient(t *testing.T) StoreClient {
	t.Helper()
	client, err := NewMongoClient(&MongoConfig{
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

// streamLoops counts the goroutines currently running a change stream
// loop of this package, recognised by their function in the stack.
func streamLoops() int {
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
		s := string(g)
		if strings.Contains(s, "db.(*mongoCollection).Watch.func") ||
			strings.Contains(s, "db.(*mongoCollection).startEventLogger.func") {
			count++
		}
	}
	return count
}

// waitStreamLoops waits until exactly want stream loops are running.
func waitStreamLoops(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := streamLoops()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d change stream loops, found %d", want, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func Test_StreamEndExpected(t *testing.T) {
	live := context.Background()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()

	closed, stop := context.WithCancel(context.Background())
	stop()

	cases := []struct {
		name   string
		ctx    context.Context
		done   context.Context
		expect bool
	}{
		{"caller and client live", live, live, false},
		{"caller cancelled", canceled, live, true},
		{"caller deadline passed", expired, live, false},
		{"client closed", live, closed, true},
		{"caller deadline passed and client closed", expired, closed, true},
		{"caller cancelled and client closed", canceled, closed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamEndExpected(tc.ctx, tc.done); got != tc.expect {
				t.Errorf("streamEndExpected = %v, expected %v", got, tc.expect)
			}
		})
	}
}

type watchCloseEvent struct {
	Name string
}

func (e *watchCloseEvent) LogEvent() {}

// Closing the client ends its watches and event loggers quietly: a panic
// in a loop goroutine would end this test binary.
func Test_CloseEndsWatches(t *testing.T) {
	before := streamLoops()

	client := newWatchTestClient(t)
	col := client.GetCollection("test", "watch-close")
	if err := col.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}

	// watches whose own contexts never end, as a table's do
	if err := col.Watch(context.Background(), nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start watch: %s", err)
	}
	if err := col.Watch(context.Background(), nil, func(string, any) {}); err != nil {
		t.Fatalf("failed to start second watch: %s", err)
	}
	if err := col.startEventLogger(context.Background(), reflect.TypeOf(watchCloseEvent{}), nil); err != nil {
		t.Fatalf("failed to start event logger: %s", err)
	}
	waitStreamLoops(t, before+3)

	if err := client.(io.Closer).Close(); err != nil {
		t.Fatalf("close failed: %s", err)
	}

	// the loops end, and none of them panics
	waitStreamLoops(t, before)
	// give a late panic the chance to surface before the test passes
	time.Sleep(500 * time.Millisecond)
}

func Test_WatchAfterClose(t *testing.T) {
	before := streamLoops()

	client := newWatchTestClient(t)
	col := client.GetCollection("test", "watch-close")
	if err := client.(io.Closer).Close(); err != nil {
		t.Fatalf("close failed: %s", err)
	}

	err := col.Watch(context.Background(), nil, func(string, any) {})
	if !errors.IsFailedPrecondition(err) {
		t.Errorf("expected FailedPrecondition from Watch after Close, got %v", err)
	}
	err = col.startEventLogger(context.Background(), reflect.TypeOf(watchCloseEvent{}), nil)
	if !errors.IsFailedPrecondition(err) {
		t.Errorf("expected FailedPrecondition from event logger after Close, got %v", err)
	}
	waitStreamLoops(t, before)
}

func Test_CloseTwice(t *testing.T) {
	client := newWatchTestClient(t)
	closer := client.(io.Closer)
	if err := closer.Close(); err != nil {
		t.Fatalf("first close failed: %s", err)
	}
	if err := closer.Close(); err != nil {
		t.Errorf("second close returned %s, expected nil", err)
	}
	disconnector := client.(interface{ Disconnect(context.Context) error })
	if err := disconnector.Disconnect(context.Background()); err != nil {
		t.Errorf("disconnect after close returned %s, expected nil", err)
	}
}

// streamEndChildEnv selects, in a child test process, the way a watch's
// stream is made to end while its client stays open.
const streamEndChildEnv = "CORE_DB_TEST_STREAM_END_CHILD"

// A stream that ends for any other reason while the client is open still
// ends the process, as before. Each case runs in a child process, since
// the panic happens in the watch goroutine and ends the whole binary.
func Test_UnexpectedStreamEndStillPanics(t *testing.T) {
	if mode := os.Getenv(streamEndChildEnv); mode != "" {
		runStreamEndChild(t, mode)
		return
	}
	for _, mode := range []string{"deadline", "undecodable-key"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^Test_UnexpectedStreamEndStillPanics$", "-test.count=1")
			cmd.Env = append(os.Environ(), streamEndChildEnv+"="+mode)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("child exited cleanly, expected a panic; output:\n%s", out)
			}
			if !strings.Contains(string(out), "End of stream observed due to error") {
				t.Fatalf("child failed without the end of stream panic: %s; output:\n%s", err, out)
			}
		})
	}
}

func runStreamEndChild(t *testing.T, mode string) {
	client := newWatchTestClient(t)
	col := client.GetCollection("test", "watch-close-child")
	if err := col.SetKeyType(reflect.TypeOf(&MyKey{})); err != nil {
		t.Fatalf("failed to set key type: %s", err)
	}

	switch mode {
	case "deadline":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := col.Watch(ctx, nil, func(string, any) {}); err != nil {
			t.Fatalf("failed to start watch: %s", err)
		}
	case "undecodable-key":
		// an _id that is not a document cannot be decoded into a key;
		// the process ends before it could clean up, so clear any
		// row an earlier run left behind first
		coll := col.(*mongoCollection).col
		badKey := bson.D{{Key: "_id", Value: "not-a-document"}}
		if _, err := coll.DeleteOne(context.Background(), badKey); err != nil {
			t.Fatalf("failed to clear: %s", err)
		}
		if err := col.Watch(context.Background(), nil, func(string, any) {}); err != nil {
			t.Fatalf("failed to start watch: %s", err)
		}
		if _, err := coll.InsertOne(context.Background(), badKey); err != nil {
			t.Fatalf("failed to insert: %s", err)
		}
	default:
		t.Fatalf("unknown mode %q", mode)
	}

	// the watch goroutine is expected to end the process before this
	time.Sleep(10 * time.Second)
}
