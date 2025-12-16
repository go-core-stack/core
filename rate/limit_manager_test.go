package rate

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"

	coreerrors "github.com/go-core-stack/core/errors"
)

func TestLimitManagerNewLimiter(t *testing.T) {
	mgr := NewLimitManager(100)

	lim, err := mgr.NewLimiter("worker", 10, 5)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}
	if lim.mgr != mgr {
		t.Fatalf("limiter manager mismatch: got %p want %p", lim.mgr, mgr)
	}
	if lim.key != "worker" {
		t.Fatalf("limiter key mismatch: got %q want %q", lim.key, "worker")
	}
	if lim.rate != 10 {
		t.Fatalf("limiter rate mismatch: got %d want %d", lim.rate, 10)
	}
	if lim.burst != 5 {
		t.Fatalf("limiter burst mismatch: got %d want %d", lim.burst, 5)
	}
	if lim.limiter.Limit() != rate.Limit(lim.rate) {
		t.Fatalf("initial limiter limit incorrect: got %v want %v", lim.limiter.Limit(), rate.Limit(lim.rate))
	}

	_, err = mgr.NewLimiter("worker", 10, 5)
	if err == nil {
		t.Fatalf("expected duplicate limiter creation to fail")
	}
	if !coreerrors.IsAlreadyExists(err) {
		t.Fatalf("expected AlreadyExists error, got %v", err)
	}
}

// TestLimitManagerUpdateInUseRedistributes ensures headroom is shared evenly
// and that limits reset when a limiter leaves the active set.
func TestLimitManagerUpdateInUseRedistributes(t *testing.T) {
	mgr := NewLimitManager(100)

	l1, err := mgr.NewLimiter("alpha", 30, 10)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}
	l2, err := mgr.NewLimiter("beta", 40, 10)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}

	l1.SetInUse(true)
	l2.SetInUse(true)

	if got := len(mgr.inUse); got != 2 {
		t.Fatalf("expected 2 active limiters, got %d", got)
	}
	if got := l1.limiter.Limit(); got < rate.Limit(30) {
		t.Fatalf("unexpected limit for alpha: got %v want more than %v", got, rate.Limit(30))
	}
	if got := l2.limiter.Limit(); got < rate.Limit(40) {
		t.Fatalf("unexpected limit for beta: got %v want more than %v", got, rate.Limit(40))
	}

	l1.SetInUse(false)

	if got := len(mgr.inUse); got != 1 {
		t.Fatalf("expected 1 active limiter after release, got %d", got)
	}
	if got := l1.limiter.Limit(); got != rate.Limit(l1.rate) {
		t.Fatalf("released limiter should reset to base rate: got %v want %v", got, rate.Limit(l1.rate))
	}
	if got := l2.limiter.Limit(); got != rate.Limit(100) {
		t.Fatalf("remaining limiter should consume full capacity: got %v want %v", got, rate.Limit(100))
	}
}

// TestLimitManagerSingleLimiterRelease verifies a single active limiter can
// claim the full capacity and returns to its base rate after release.
func TestLimitManagerSingleLimiterRelease(t *testing.T) {
	mgr := NewLimitManager(100)

	l, err := mgr.NewLimiter("solo", 30, 10)
	if err != nil {
		t.Fatalf("unexpected error creating limiter: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetInUse should not panic on release: %v", r)
		}
	}()

	l.SetInUse(true)
	if got := l.limiter.Limit(); got != rate.Limit(100) {
		t.Fatalf("expected limiter to receive full capacity when active: got %v want %v", got, rate.Limit(100))
	}

	l.SetInUse(false)
	if len(mgr.inUse) != 0 {
		t.Fatalf("expected no active limiters after release, got %d", len(mgr.inUse))
	}
	if got := l.limiter.Limit(); got != rate.Limit(l.rate) {
		t.Fatalf("expected limiter to reset to base rate after release: got %v want %v", got, rate.Limit(l.rate))
	}
}

// TestNewLimiterInvalidBurst verifies validation of burst size.
func TestNewLimiterInvalidBurst(t *testing.T) {
	mgr := NewLimitManager(100)

	tests := []struct {
		name  string
		burst int64
	}{
		{"zero burst", 0},
		{"negative burst", -1},
		{"large negative burst", -1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mgr.NewLimiter("test", 10, tt.burst)
			if err == nil {
				t.Fatalf("expected error for burst=%d, got nil", tt.burst)
			}
			if !coreerrors.IsInvalidArgument(err) {
				t.Fatalf("expected InvalidArgument error, got %v", err)
			}
		})
	}
}

// TestNewLimiterMinimumBurst verifies burst size of 1 works.
func TestNewLimiterMinimumBurst(t *testing.T) {
	mgr := NewLimitManager(100)

	lim, err := mgr.NewLimiter("min", 10, 1)
	if err != nil {
		t.Fatalf("unexpected error with burst=1: %v", err)
	}
	if lim.burst != 1 {
		t.Fatalf("expected burst=1, got %d", lim.burst)
	}
}

// TestWrapReaderNotFound verifies error when wrapping with non-existent limiter.
func TestWrapReaderNotFound(t *testing.T) {
	mgr := NewLimitManager(100)
	rc := io.NopCloser(strings.NewReader("test"))

	_, err := mgr.WrapReader(context.Background(), "nonexistent", rc)
	if err == nil {
		t.Fatal("expected error for non-existent limiter")
	}
	if !coreerrors.IsNotFound(err) {
		t.Fatalf("expected NotFound error, got %v", err)
	}
}

// TestWrapHTTPResponseWriterNotFound verifies error when wrapping with non-existent limiter.
func TestWrapHTTPResponseWriterNotFound(t *testing.T) {
	mgr := NewLimitManager(100)
	w := httptest.NewRecorder()

	_, err := mgr.WrapHTTPResponseWriter(context.Background(), "nonexistent", w)
	if err == nil {
		t.Fatal("expected error for non-existent limiter")
	}
	if !coreerrors.IsNotFound(err) {
		t.Fatalf("expected NotFound error, got %v", err)
	}
}

// TestRateLimitedReader verifies rate limiting behavior for readers.
func TestRateLimitedReader(t *testing.T) {
	mgr := NewLimitManager(1000) // 1000 bytes/sec
	_, err := mgr.NewLimiter("reader", 1000, 100)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	data := bytes.Repeat([]byte("a"), 500)
	rc := io.NopCloser(bytes.NewReader(data))

	rlReader, err := mgr.WrapReader(context.Background(), "reader", rc)
	if err != nil {
		t.Fatalf("failed to wrap reader: %v", err)
	}
	defer rlReader.Close()

	start := time.Now()
	buf := make([]byte, 500)
	n, err := io.ReadFull(rlReader, buf)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if n != 500 {
		t.Fatalf("expected to read 500 bytes, got %d", n)
	}

	// At 1000 bytes/sec, reading 500 bytes should take ~0.5s (with some tolerance)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("read completed too fast (%v), rate limiting likely broken", elapsed)
	}
	if elapsed < 400*time.Millisecond {
		t.Logf("warning: read completed faster than expected (%v), rate limiting may not be working", elapsed)
	}
}

// TestRateLimitedReaderChunking verifies reading chunks larger than burst size.
func TestRateLimitedReaderChunking(t *testing.T) {
	mgr := NewLimitManager(1000)
	_, err := mgr.NewLimiter("reader", 1000, 50) // burst of 50 bytes
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	data := bytes.Repeat([]byte("a"), 200)
	rc := io.NopCloser(bytes.NewReader(data))

	rlReader, err := mgr.WrapReader(context.Background(), "reader", rc)
	if err != nil {
		t.Fatalf("failed to wrap reader: %v", err)
	}
	defer rlReader.Close()

	// Try to read 100 bytes at once (larger than burst of 50)
	buf := make([]byte, 100)
	n, err := rlReader.Read(buf)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	// Should read at most burst size (50) per call
	if n > 50 {
		t.Fatalf("expected to read at most 50 bytes (burst size), got %d", n)
	}
}

// TestRateLimitedReaderContextCancellation verifies context cancellation.
func TestRateLimitedReaderContextCancellation(t *testing.T) {
	mgr := NewLimitManager(10) // Very slow rate
	_, err := mgr.NewLimiter("reader", 10, 5)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	data := bytes.Repeat([]byte("a"), 1000)
	rc := io.NopCloser(bytes.NewReader(data))

	ctx, cancel := context.WithCancel(context.Background())
	rlReader, err := mgr.WrapReader(ctx, "reader", rc)
	if err != nil {
		t.Fatalf("failed to wrap reader: %v", err)
	}
	defer rlReader.Close()

	// Cancel context immediately
	cancel()

	buf := make([]byte, 100)
	_, err = rlReader.Read(buf)
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
}

// TestRateLimitedReaderClose verifies SetInUse is called on close.
func TestRateLimitedReaderClose(t *testing.T) {
	mgr := NewLimitManager(1000)
	_, err := mgr.NewLimiter("reader", 1000, 100)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	rc := io.NopCloser(strings.NewReader("test"))
	rlReader, err := mgr.WrapReader(context.Background(), "reader", rc)
	if err != nil {
		t.Fatalf("failed to wrap reader: %v", err)
	}

	// After wrapping, limiter should be in use
	if len(mgr.inUse) != 1 {
		t.Fatalf("expected 1 in-use limiter, got %d", len(mgr.inUse))
	}

	rlReader.Close()

	// After closing, limiter should not be in use
	if len(mgr.inUse) != 0 {
		t.Fatalf("expected 0 in-use limiters after close, got %d", len(mgr.inUse))
	}
}

// TestRateLimitedHTTPResponseWriter verifies rate limiting for HTTP writers.
func TestRateLimitedHTTPResponseWriter(t *testing.T) {
	mgr := NewLimitManager(1000) // 1000 bytes/sec
	_, err := mgr.NewLimiter("writer", 1000, 100)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	w := httptest.NewRecorder()
	rlWriter, err := mgr.WrapHTTPResponseWriter(context.Background(), "writer", w)
	if err != nil {
		t.Fatalf("failed to wrap writer: %v", err)
	}
	defer rlWriter.Close()

	data := bytes.Repeat([]byte("a"), 500)
	start := time.Now()
	n, err := rlWriter.Write(data)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != 500 {
		t.Fatalf("expected to write 500 bytes, got %d", n)
	}

	// At 1000 bytes/sec, writing 500 bytes should take ~0.5s (with some tolerance)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("write completed too fast (%v), rate limiting likely broken", elapsed)
	}
	if elapsed < 400*time.Millisecond {
		t.Logf("warning: write completed faster than expected (%v), rate limiting may not be working", elapsed)
	}

	if w.Body.Len() != 500 {
		t.Fatalf("expected 500 bytes in response body, got %d", w.Body.Len())
	}
}

// TestRateLimitedHTTPResponseWriterChunking verifies writing chunks larger than burst.
func TestRateLimitedHTTPResponseWriterChunking(t *testing.T) {
	mgr := NewLimitManager(1000)
	_, err := mgr.NewLimiter("writer", 1000, 50) // burst of 50 bytes
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	w := httptest.NewRecorder()
	rlWriter, err := mgr.WrapHTTPResponseWriter(context.Background(), "writer", w)
	if err != nil {
		t.Fatalf("failed to wrap writer: %v", err)
	}
	defer rlWriter.Close()

	// Write 200 bytes (larger than burst of 50)
	data := bytes.Repeat([]byte("a"), 200)
	n, err := rlWriter.Write(data)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != 200 {
		t.Fatalf("expected to write 200 bytes, got %d", n)
	}

	// All data should be written despite chunking
	if w.Body.Len() != 200 {
		t.Fatalf("expected 200 bytes in response body, got %d", w.Body.Len())
	}
}

// TestRateLimitedHTTPResponseWriterContextCancellation verifies context cancellation.
func TestRateLimitedHTTPResponseWriterContextCancellation(t *testing.T) {
	mgr := NewLimitManager(10) // Very slow rate
	_, err := mgr.NewLimiter("writer", 10, 5)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	w := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	rlWriter, err := mgr.WrapHTTPResponseWriter(ctx, "writer", w)
	if err != nil {
		t.Fatalf("failed to wrap writer: %v", err)
	}
	defer rlWriter.Close()

	// Cancel context immediately
	cancel()

	data := bytes.Repeat([]byte("a"), 100)
	_, err = rlWriter.Write(data)
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
}

// TestRateLimitedHTTPResponseWriterClose verifies SetInUse is called on close.
func TestRateLimitedHTTPResponseWriterClose(t *testing.T) {
	mgr := NewLimitManager(1000)
	_, err := mgr.NewLimiter("writer", 1000, 100)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	w := httptest.NewRecorder()
	rlWriter, err := mgr.WrapHTTPResponseWriter(context.Background(), "writer", w)
	if err != nil {
		t.Fatalf("failed to wrap writer: %v", err)
	}

	// After wrapping, limiter should be in use
	if len(mgr.inUse) != 1 {
		t.Fatalf("expected 1 in-use limiter, got %d", len(mgr.inUse))
	}

	rlWriter.Close()

	// After closing, limiter should not be in use
	if len(mgr.inUse) != 0 {
		t.Fatalf("expected 0 in-use limiters after close, got %d", len(mgr.inUse))
	}
}

// TestRateLimitedHTTPResponseWriterHeaders verifies header operations work.
func TestRateLimitedHTTPResponseWriterHeaders(t *testing.T) {
	mgr := NewLimitManager(1000)
	_, err := mgr.NewLimiter("writer", 1000, 100)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	w := httptest.NewRecorder()
	rlWriter, err := mgr.WrapHTTPResponseWriter(context.Background(), "writer", w)
	if err != nil {
		t.Fatalf("failed to wrap writer: %v", err)
	}
	defer rlWriter.Close()

	rlWriter.Header().Set("Content-Type", "text/plain")
	rlWriter.WriteHeader(http.StatusOK)
	rlWriter.Write([]byte("test"))

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("expected Content-Type=text/plain, got %q", ct)
	}
}

// TestConcurrentReaderAccess verifies concurrent reader access.
func TestConcurrentReaderAccess(t *testing.T) {
	mgr := NewLimitManager(10000)
	_, err := mgr.NewLimiter("reader", 10000, 100)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	var wg sync.WaitGroup
	readers := 5

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			data := bytes.Repeat([]byte("a"), 100)
			rc := io.NopCloser(bytes.NewReader(data))
			rlReader, err := mgr.WrapReader(context.Background(), "reader", rc)
			if err != nil {
				t.Errorf("failed to wrap reader: %v", err)
				return
			}
			defer rlReader.Close()

			buf := make([]byte, 100)
			_, err = io.ReadFull(rlReader, buf)
			if err != nil {
				t.Errorf("read failed: %v", err)
			}
		}()
	}

	wg.Wait()

	// Verify all limiters released
	if len(mgr.inUse) != 0 {
		t.Fatalf("expected 0 in-use limiters after all readers closed, got %d", len(mgr.inUse))
	}
}

// TestConcurrentWriterAccess verifies concurrent writer access.
func TestConcurrentWriterAccess(t *testing.T) {
	mgr := NewLimitManager(10000)
	_, err := mgr.NewLimiter("writer", 10000, 100)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}

	var wg sync.WaitGroup
	writers := 5

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			w := httptest.NewRecorder()
			rlWriter, err := mgr.WrapHTTPResponseWriter(context.Background(), "writer", w)
			if err != nil {
				t.Errorf("failed to wrap writer: %v", err)
				return
			}
			defer rlWriter.Close()

			data := bytes.Repeat([]byte("a"), 100)
			_, err = rlWriter.Write(data)
			if err != nil {
				t.Errorf("write failed: %v", err)
			}
		}()
	}

	wg.Wait()

	// Verify all limiters released
	if len(mgr.inUse) != 0 {
		t.Fatalf("expected 0 in-use limiters after all writers closed, got %d", len(mgr.inUse))
	}
}
