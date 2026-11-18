// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-core-stack/core/db"
)

// watchLoops counts the goroutines currently running a collection watch
// loop, recognised by their function in the stack.
func watchLoops() int {
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
		if strings.Contains(string(g), "db.(*mongoCollection).Watch.func") {
			count++
		}
	}
	return count
}

func waitWatchLoops(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := watchLoops()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d watch loops, found %d", want, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A Table and a CachedTable watch with a context that never ends, so
// closing their client is what ends their watches. Ending them must not
// panic: a panic in a watch goroutine would end this test binary.
func Test_CloseEndsTableWatches(t *testing.T) {
	before := watchLoops()

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
	s := client.GetDataStore("test")

	tbl := &ProductTable{}
	if err := tbl.Initialize(s.GetCollection("close-products-table")); err != nil {
		t.Fatalf("failed to initialize table: %s", err)
	}
	cached := &MyTable{}
	if err := cached.Initialize(s.GetCollection("close-cached-table")); err != nil {
		t.Fatalf("failed to initialize cached table: %s", err)
	}
	waitWatchLoops(t, before+2)

	if err := client.(io.Closer).Close(); err != nil {
		t.Fatalf("close failed: %s", err)
	}

	waitWatchLoops(t, before)
	// give a late panic the chance to surface before the test passes
	time.Sleep(500 * time.Millisecond)
}
