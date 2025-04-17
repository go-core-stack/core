// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package reconciler

import (
	"context"
	"sync"
	"time"
)

// Since Reconciler Pipeline will be used across go routines, it is
// quite possible to have producers and consumers to work at
// different speeds with a possibility of having backlogs or causing
// holdups, thus by default use a buffer length of 1024 for every
// Pipeline to ensure producers can just work seemlessly under
// regular scenarios
// Note: this is expected to be consumed only locally
const bufferLength = 1024

// Pipeline of elements to be processed by reconciler upon notification
type Pipeline struct {
	// context under which the pipeline is working
	// where the context closure means the pipeline is stopped
	ctx context.Context

	// map of entries to work with, here we are storing entries in a map
	// to enable possibility of compressing notifications while trying
	// to enqueue an entry which is already in pipeline
	pMap sync.Map

	// Pipeline is internally built on a buffered channel internally
	pChannel chan any

	// reconciler function to trigger while processing an entry in the
	// pipeline
	reconciler reconcilerFunc
}

func (p *Pipeline) Enqueue(k any) error {
	// do not allow if the context is already closed
	if p.ctx.Err() != nil {
		return p.ctx.Err()
	}

	// load or store the entry to sync map, checking existence of the
	// entry in the Pipeline, ensuring compressing multiple
	// notifications for a single entry into one
	// while the value stored is nil as we are treating this map more
	// of as a set, where values do not hold relevance as of now
	_, loaded := p.pMap.LoadOrStore(k, nil)
	if !loaded {
		// if entry didn't exist in the map, ensure pushing the same
		// to the buffered channel for processing by reconciler
		p.pChannel <- k
	}

	return nil
}

// initialize and start the pipeline processing
// internal function and should not be exposed outside
func (p *Pipeline) initialize() {
	for {
		select {
		case <-p.ctx.Done():
			// pipeline processing is stopped return from here
			return
		case k := <-p.pChannel:
			// process the entry available in the pipeline
			// send it over to the reconciler for processing
			// delete the key from the map while triggering
			// the reconciler
			p.pMap.Delete(k)

			// trigger the reconciler
			res, err := p.reconciler(k)
			if err != nil {
				// there was an error while processing the entry
				// requeue it at the back of the pipeline for
				// processing later
				_ = p.Enqueue(k)
			} else {
				if res != nil && res.RequeueAfter != 0 {
					go func(k1 any) {
						// requeue the entry after specified time
						time.Sleep(res.RequeueAfter)
						_ = p.Enqueue(k1)
					}(k)
				}
			}
		}
	}
}

// Creates a New Pipeline for queuing up and processing entries provided
// for reconciliation
func NewPipeline(ctx context.Context, fn reconcilerFunc) *Pipeline {
	p := &Pipeline{
		ctx:        ctx,
		pMap:       sync.Map{},
		pChannel:   make(chan any, bufferLength),
		reconciler: fn,
	}

	// initialize the pipeline before passing it externally
	// to start the core functionality
	go p.initialize()
	return p
}
