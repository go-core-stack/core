// Copyright © 2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"context"
	"log"
	"reflect"
	"sync"
	"sync/atomic"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/go-core-stack/core/errors"
)

// A client follows every collection of one database through a single change
// stream on that database, however many watches its callers start there.
// The database pays for each open stream all the time, a read held open per
// stream and every write examined once per stream, so the cost of following
// one more collection, table or lock is a registration on a stream that is
// already open, not another stream.
//
// The stream returns every change in the database, and the process picks
// out the ones each watch asks for. It is opened by the first watch on the
// database and stays as it is until the last one ends, so starting or
// ending a watch never pauses the others, and a watch receives every change
// made after Watch returns, since the stream was already reading when it
// started. It may also receive changes made shortly before it started,
// which the stream had not yet given out. The cost of not filtering on the server is
// that every change in the database, followed or not, reaches the process.
//
// A followed collection dropped, renamed or replaced by another renamed
// onto it, or its database dropped, ends the process whatever the
// watches' filters, as it ended a stream on the collection.
//
// The process gives each change to every watch on its collection whose
// filter matches it (see eventMatcher), through a queue of that watch's
// own, so a slow callback delays only its own changes, which it receives in
// order. The queue has no bound: a callback that does not return keeps its
// watch's changes in memory until it does, and the queue reaching
// backlogWarning changes is logged.

// sharedStream is the change stream a client holds on one database, and
// the watches it serves. Everything but regs is guarded by the client's
// watchMu.
type sharedStream struct {
	client *mongoClient
	db     *mongo.Database

	// regs is what the reader routes to; it is replaced, never changed
	regs atomic.Pointer[[]*watchRegistration]

	cs *mongo.ChangeStream
	// cancel ends the reader, which closes done
	cancel context.CancelFunc
	done   chan struct{}
	// tracked is the stream's cursor, which ending it kills (see
	// openStream)
	tracked *openStream
}

// watchRegistration is one Watch on a shared stream.
type watchRegistration struct {
	stream  *sharedStream
	col     string
	matcher eventMatcher // nil without a filter
	keyType reflect.Type
	cb      WatchCallbackfn

	ctx     context.Context // the caller's
	runCtx  context.Context // ends with the caller's or when the client closes
	release func()

	mu      sync.Mutex
	pending []watchEvent
	wake    chan struct{}
}

// backlogWarning is how many changes waiting for a watch's callback are
// logged, once each time its queue grows to it, so a callback that has
// stopped returning shows before the process runs out of memory.
const backlogWarning = 10000

type watchEvent struct {
	op  string
	key any
}

// watchShared starts a watch on the client's shared stream for the
// collection's database, opening the stream if it is the first.
func (c *mongoCollection) watchShared(ctx context.Context, matcher eventMatcher, cb WatchCallbackfn) error {
	client := c.parent.client
	runCtx, release, err := client.streamContext(ctx)
	if err != nil {
		return err
	}
	reg := &watchRegistration{
		col:     c.colName,
		matcher: matcher,
		// a snapshot, so a later SetKeyType on this collection does not
		// race the routing of a change
		keyType: c.keyType,
		cb:      cb,
		ctx:     ctx,
		runCtx:  runCtx,
		release: release,
		wake:    make(chan struct{}, 1),
	}
	if err := client.register(c.parent.db, reg); err != nil {
		release()
		return err
	}
	go reg.run()
	return nil
}

// register adds reg to the shared stream on db, opening the stream when
// reg is its first watch.
func (c *mongoClient) register(db *mongo.Database, reg *watchRegistration) error {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()

	// checked once the lock is held, so a caller whose context ended
	// while it waited gets the error, as opening a stream of its own
	// under ctx would
	if err := reg.ctx.Err(); err != nil {
		return err
	}
	if c.done.Err() != nil {
		return errors.Wrap(errors.FailedPrecondition, "client is closed")
	}
	s := c.shared[db.Name()]
	if s == nil {
		s = &sharedStream{client: c, db: db}
		s.regs.Store(&[]*watchRegistration{})
		if err := s.open(reg.ctx); err != nil {
			return c.streamOpenError(err)
		}
		if c.shared == nil {
			c.shared = map[string]*sharedStream{}
		}
		c.shared[db.Name()] = s
	}
	reg.stream = s
	regs := append(append([]*watchRegistration{}, *s.regs.Load()...), reg)
	s.regs.Store(&regs)
	return nil
}

// unregister removes reg, and ends the shared stream when no watch is left.
func (c *mongoClient) unregister(reg *watchRegistration) {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()

	s := reg.stream
	var regs []*watchRegistration
	for _, r := range *s.regs.Load() {
		if r != reg {
			regs = append(regs, r)
		}
	}
	s.regs.Store(&regs)
	if len(regs) == 0 {
		s.stop()
		delete(c.shared, s.db.Name())
	}
}

// stop ends the open stream on purpose.
func (s *sharedStream) stop() {
	s.cancel()
	<-s.done
	// bounded, since the client's other shared watches wait on it
	ctx, cancel := context.WithTimeout(context.Background(), killCursorTimeout)
	_ = s.cs.Close(ctx)
	cancel()
	s.client.endStream(s.tracked)
}

// open starts the stream on the database, giving up when caller ends
// first.
func (s *sharedStream) open(caller context.Context) error {
	opts := options.ChangeStream().SetMaxAwaitTime(s.client.maxAwaitTime)
	ctx, cancel := context.WithCancel(s.client.done)
	// opening is bounded by the caller as well, while the stream it opens
	// lives with the client
	opening, endOpening := context.WithCancel(ctx)
	stopAfter := context.AfterFunc(caller, endOpening)
	cs, err := s.db.Watch(opening, mongo.Pipeline{}, opts)
	stopAfter()
	endOpening()
	if err != nil {
		cancel()
		return err
	}
	s.cs, s.cancel, s.done = cs, cancel, make(chan struct{})
	// a database's stream reads from its $cmd.aggregate namespace
	s.tracked = s.client.trackStream(s.db, "$cmd.aggregate", cs)
	go s.read(ctx)
	return nil
}

// read gives each change the stream returns to the watches asking for it.
// A stream that ends while it was neither stopped nor its client closed
// ends the process, as a watch's stream always has, so no watch is left
// on, or later joins, a stream that has ended.
func (s *sharedStream) read(ctx context.Context) {
	defer close(s.done)
	for s.tracked.next(ctx, s.cs) {
		s.route(s.cs.Current)
	}
	if ctx.Err() != nil {
		return
	}
	log.Panicf("End of stream observed due to error %s", s.cs.Err())
}

// route queues a change for every watch on its collection whose filter
// matches it.
func (s *sharedStream) route(event bson.Raw) {
	col, _ := event.Lookup("ns", "coll").StringValueOK()
	op, ok := event.Lookup("operationType").StringValueOK()
	// a rename with dropTarget replaces the collection it is renamed to;
	// a rename into another database reaches this stream as a drop
	to, _ := event.Lookup("to", "coll").StringValueOK()
	for _, reg := range *s.regs.Load() {
		// the client closing ends its watches' contexts after it is
		// marked closed, so a drop read meanwhile is not a lifecycle end
		if reg.runCtx.Err() != nil || s.client.done.Err() != nil {
			continue
		}
		// a watch started on a collection dropped before, whose drop the
		// stream had not yet given out, ends the process too
		if op == "dropDatabase" || (reg.col == col && (op == "drop" || op == "rename")) || (op == "rename" && reg.col == to) {
			log.Panicf("End of stream observed due to error, %s of %s.%s followed by a watch", op, s.db.Name(), col)
		}
		if reg.col != col {
			continue
		}
		if reg.matcher != nil && !reg.matcher.matches(event) {
			continue
		}
		if !ok {
			log.Panicf("End of stream observed due to error, unable to find operation type of a change on %s.%s", s.db.Name(), col)
		}
		key, err := reg.decodeKey(event)
		if err != nil {
			log.Panicf("End of stream observed due to error %s, in a %s change on %s.%s", err, op, s.db.Name(), col)
		}
		reg.push(watchEvent{op: op, key: key})
	}
}

// decodeKey decodes the changed document's _id into the watch's key type,
// or into a bson.M when none is set.
func (reg *watchRegistration) decodeKey(event bson.Raw) (any, error) {
	id := event.Lookup("documentKey", "_id")
	if id.Type != bson.TypeEmbeddedDocument {
		return nil, errors.Wrapf(errors.InvalidArgument, "unable to find a document id in a %s change", id.Type)
	}
	var key any
	if reg.keyType != nil {
		key = reflect.New(reg.keyType.Elem()).Interface()
	} else {
		key = &bson.M{}
	}
	if err := bson.Unmarshal(id.Value, key); err != nil {
		return nil, err
	}
	if reg.keyType == nil {
		return *key.(*bson.M), nil
	}
	return key, nil
}

func (reg *watchRegistration) push(ev watchEvent) {
	reg.mu.Lock()
	reg.pending = append(reg.pending, ev)
	if len(reg.pending) == backlogWarning {
		log.Printf("watch on %s.%s has %d changes waiting for its callback", reg.stream.db.Name(), reg.col, backlogWarning)
	}
	reg.mu.Unlock()
	select {
	case reg.wake <- struct{}{}:
	default:
	}
}

// run calls the callback for each queued change, in order, until the
// caller's context ends or the client is closed. Any other end of the
// caller's context, such as its deadline passing, ends the process, as it
// did when each watch had its own stream.
func (reg *watchRegistration) run() {
	client := reg.stream.client
	defer client.unregister(reg)
	defer reg.release()
	for {
		select {
		case <-reg.runCtx.Done():
			if !streamEndExpected(reg.ctx, client.done) {
				log.Panicf("End of stream observed due to error %s", reg.ctx.Err())
			}
			return
		case <-reg.wake:
		}
		reg.mu.Lock()
		pending := reg.pending
		reg.pending = nil
		reg.mu.Unlock()
		for _, ev := range pending {
			if reg.runCtx.Err() != nil {
				break
			}
			reg.cb(ev.op, ev.key)
		}
	}
}
