// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package reconciler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-core-stack/core/errors"
)

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

// waitGoroutines waits until the goroutines with frame number want.
func waitGoroutines(t *testing.T, frame string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := goroutinesIn(frame)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d goroutines in %s, found %d", want, frame, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const (
	pipelineLoop = "reconciler.(*Pipeline).initialize("
	requeueWait  = "reconciler.(*Pipeline).initialize.func1("
	enqueueCall  = "reconciler.(*Pipeline).Enqueue("
	replayLoop   = "reconciler.(*ManagerImpl).Register.func1("
)

// keysParent is a Manager without KeyLister.
type keysParent struct {
	ManagerImpl
	keys []any
}

func (p *keysParent) ReconcilerGetAllKeys() []any {
	return p.keys
}

// listerParent is a Manager with KeyLister, whose listing is list.
type listerParent struct {
	ManagerImpl
	list func(ctx context.Context) ([]any, error)
}

func (p *listerParent) ReconcilerGetAllKeys() []any {
	panic("ReconcilerGetAllKeys must not be called for a KeyLister")
}

func (p *listerParent) ReconcilerListKeys(ctx context.Context) ([]any, error) {
	return p.list(ctx)
}

// gatedController blocks every Reconcile until gate is closed and counts
// the calls that started.
type gatedController struct {
	gate    chan struct{}
	started atomic.Int64
}

func (c *gatedController) Reconcile(k any) (*Result, error) {
	c.started.Add(1)
	<-c.gate
	return &Result{}, nil
}

// Once the manager's context has ended, a change notification is dropped
// rather than panicking.
func Test_NotifyAfterEndIsDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &keysParent{}
	if err := m.Initialize(ctx, m); err != nil {
		t.Fatalf("initialize: %s", err)
	}
	if err := m.Register("c", &gatedController{gate: make(chan struct{})}); err != nil {
		t.Fatalf("register: %s", err)
	}
	cancel()
	m.NotifyCallback("k")
}

// A notification that is waiting for room in a full pipeline when the
// manager ends returns, quietly.
func Test_NotifyWaitingWhenEndedReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &keysParent{}
	if err := m.Initialize(ctx, m); err != nil {
		t.Fatalf("initialize: %s", err)
	}
	crtl := &gatedController{gate: make(chan struct{})}
	before := goroutinesIn(enqueueCall)
	loops := goroutinesIn(pipelineLoop)
	if err := m.Register("c", crtl); err != nil {
		t.Fatalf("register: %s", err)
	}

	// one key is taken by the blocked Reconcile, bufferLength fill the
	// channel, the next notification waits
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < bufferLength+2; i++ {
			m.NotifyCallback(i)
		}
	}()
	waitGoroutines(t, enqueueCall, before+1)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("notification still waiting after the manager ended")
	}
	close(crtl.gate)
	waitGoroutines(t, pipelineLoop, loops)
}

// The replay listing of a KeyLister reads under the manager's context: a
// listing that only ends with its context ends when the manager ends, and
// the replay returns quietly.
func Test_ReplayListingEndsWithManager(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listing := make(chan struct{})
	m := &listerParent{list: func(lctx context.Context) ([]any, error) {
		close(listing)
		<-lctx.Done()
		return nil, lctx.Err()
	}}
	if err := m.Initialize(ctx, m); err != nil {
		t.Fatalf("initialize: %s", err)
	}
	before := goroutinesIn(replayLoop)
	if err := m.Register("c", &gatedController{gate: make(chan struct{})}); err != nil {
		t.Fatalf("register: %s", err)
	}
	<-listing
	cancel()
	waitGoroutines(t, replayLoop, before)
	// give a late panic the chance to surface
	time.Sleep(200 * time.Millisecond)
}

// A replay that is waiting for room in a full pipeline when the manager
// ends returns, quietly.
func Test_ReplayWaitingWhenEndedReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	keys := make([]any, 2*bufferLength)
	for i := range keys {
		keys[i] = i
	}
	m := &keysParent{keys: keys}
	if err := m.Initialize(ctx, m); err != nil {
		t.Fatalf("initialize: %s", err)
	}
	crtl := &gatedController{gate: make(chan struct{})}
	before := goroutinesIn(replayLoop)
	waiting := goroutinesIn(enqueueCall)
	loops := goroutinesIn(pipelineLoop)
	if err := m.Register("c", crtl); err != nil {
		t.Fatalf("register: %s", err)
	}
	waitGoroutines(t, enqueueCall, waiting+1)
	cancel()
	waitGoroutines(t, replayLoop, before)
	close(crtl.gate)
	waitGoroutines(t, pipelineLoop, loops)
	time.Sleep(200 * time.Millisecond)
}

// Once a pipeline has stopped, no entry already in it is reconciled. A
// select has no priority, so a loop without the check would still pick
// the next entry half the time; twenty pipelines make that certain to show.
func Test_PipelineStopsBeforeNextEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	crtls := make([]*gatedController, 20)
	for i := range crtls {
		crtl := &gatedController{gate: make(chan struct{})}
		crtls[i] = crtl
		p := NewPipeline(ctx, crtl.Reconcile)
		for k := 0; k < 100; k++ {
			if err := p.Enqueue(k); err != nil {
				t.Fatalf("enqueue: %s", err)
			}
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, crtl := range crtls {
		for crtl.started.Load() != 1 {
			if time.Now().After(deadline) {
				t.Fatalf("pipeline did not start reconciling")
			}
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	for _, crtl := range crtls {
		close(crtl.gate)
	}
	time.Sleep(200 * time.Millisecond)
	for i, crtl := range crtls {
		if got := crtl.started.Load(); got != 1 {
			t.Fatalf("pipeline %d: %d entries reconciled after it stopped", i, got-1)
		}
	}
}

// Register on a manager whose context has ended fails, rather than
// starting a pipeline that never runs.
func Test_RegisterAfterEndFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &keysParent{}
	if err := m.Initialize(ctx, m); err != nil {
		t.Fatalf("initialize: %s", err)
	}
	cancel()
	before := goroutinesIn(pipelineLoop)
	err := m.Register("c", &gatedController{gate: make(chan struct{})})
	if !errors.IsFailedPrecondition(err) {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
	if got := goroutinesIn(pipelineLoop); got != before {
		t.Fatalf("a pipeline was started")
	}
}

// A requeue waiting for its delay ends when the pipeline stops, and never
// enqueues afterwards.
func Test_RequeueEndsWithPipeline(t *testing.T) {
	before := goroutinesIn(requeueWait)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	p := NewPipeline(ctx, func(k any) (*Result, error) {
		calls.Add(1)
		return &Result{RequeueAfter: time.Hour}, nil
	})
	for i := 0; i < 10; i++ {
		if err := p.Enqueue(i); err != nil {
			t.Fatalf("enqueue: %s", err)
		}
	}
	waitGoroutines(t, requeueWait, before+10)
	cancel()
	waitGoroutines(t, requeueWait, before)
	if got := calls.Load(); got != 10 {
		t.Fatalf("expected 10 reconciles, got %d", got)
	}
}

const listingChildEnv = "RECONCILER_LISTING_CHILD"

// While the manager's context has not ended, a failed listing panics as
// it did before the listing could end with the manager.
func Test_ReplayListingErrorStillPanics(t *testing.T) {
	if os.Getenv(listingChildEnv) != "" {
		m := &listerParent{list: func(context.Context) ([]any, error) {
			return nil, fmt.Errorf("listing failed")
		}}
		if err := m.Initialize(context.Background(), m); err != nil {
			t.Fatalf("initialize: %s", err)
		}
		if err := m.Register("c", &gatedController{gate: make(chan struct{})}); err != nil {
			t.Fatalf("register: %s", err)
		}
		// the replay goroutine is expected to end the process before this
		time.Sleep(5 * time.Second)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^Test_ReplayListingErrorStillPanics$", "-test.count=1")
	cmd.Env = append(os.Environ(), listingChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child exited cleanly, expected a panic; output:\n%s", out)
	}
	if !strings.Contains(string(out), "got error while fetching all keys listing failed") {
		t.Fatalf("child failed without the listing panic: %s; output:\n%s", err, out)
	}
}
