// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package reconciler

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/go-core-stack/core/errors"
)

// Taking motivation from kubernetes
// https://github.com/kubernetes-sigs/controller-runtime/blob/main/pkg/reconcile/reconcile.go
// enable a reconciler function
type Result struct {
	// RequeueAfter if greater than 0, tells the Controller to requeue the reconcile key after the Duration.
	RequeueAfter time.Duration
}

type Request struct {
	Key any
}

type reconcilerFunc func(k any) (*Result, error)

// controller interface meant for registering to database manager
// for processing changes inoccuring to varies entries in the database
type Controller interface {
	Reconcile(k any) (*Result, error)
}

// Controller data used for saving the context of a controller
// and corresponding information along with the reconciliation
// pipeline
type controllerData struct {
	name     string
	handle   Controller
	pipeline *Pipeline
}

// Manager interface for enforcing implementation of specific
// functions
type Manager interface {
	// function to get all existing keys in the collection
	ReconcilerGetAllKeys() []any

	// interface should not be embed by anyone directly
	mustEmbedManagerImpl()
}

// KeyLister is optionally implemented by a Manager. When it is, the
// replay of existing keys started by Register lists them with
// ReconcilerListKeys under the context the ManagerImpl was initialized
// with, so the listing ends when that context ends. Otherwise the replay
// calls ReconcilerGetAllKeys.
type KeyLister interface {
	// ReconcilerListKeys returns all existing keys in the collection,
	// reading under ctx. It reports a failed read as an error.
	ReconcilerListKeys(ctx context.Context) ([]any, error)
}

// Manager implementation with implementation of the core logic
// typically built over and above database store on which it will
// offer reconcilation capabilities
type ManagerImpl struct {
	Manager
	parent      Manager
	controllers sync.Map
	ctx         context.Context
}

// callback registered with the data store
//
// Once the context the manager was initialized with has ended, its
// pipelines have stopped and the entry is dropped quietly.
func (m *ManagerImpl) NotifyCallback(wKey any) {
	// iterate over all the registered clients
	m.controllers.Range(func(name, data any) bool {
		crtl, ok := data.(*controllerData)
		if !ok {
			// this ideally should never happen
			log.Panicln("Wrong data type of controller info received")
		}
		// enqueue the entry for reconciliation
		err := crtl.pipeline.Enqueue(wKey)
		if err != nil {
			if m.ctx.Err() != nil {
				// the manager has ended, so every pipeline has
				// stopped; drop the entry
				return false
			}
			// Enqueue fails only once its context, the manager's,
			// has ended; this is kept for any other failure
			log.Panicln("Failed to enqueue an entry for reconciliation", name, err)
		}
		return true
	})
}

// Initialize the manager with context and relevant collection to work with
//
// ctx is the manager's lifetime. When it ends, the pipelines of the
// controllers registered with it stop, their requeues end, a change
// notification is dropped, and the replay of existing keys started by
// Register returns. The replay's listing is read under ctx only if the
// parent implements KeyLister; ReconcilerGetAllKeys is not bounded by it.
// Register fails once ctx has ended.
func (m *ManagerImpl) Initialize(ctx context.Context, parent Manager) error {
	if m.parent != nil {
		return errors.Wrap(errors.AlreadyExists, "Initialization already done")
	}

	m.ctx = ctx
	m.parent = parent

	return nil
}

// register a controller with manager for reconciliation
func (m *ManagerImpl) Register(name string, crtl Controller) error {
	if m.parent == nil {
		return errors.Wrap(errors.InvalidArgument, "manager is not initialized")
	}
	if m.ctx.Err() != nil {
		// a pipeline started now would never run; for a table this
		// means its client has been closed
		return errors.Wrap(errors.FailedPrecondition, "reconciler manager has ended")
	}
	data := &controllerData{
		name:   name,
		handle: crtl,
	}
	_, loaded := m.controllers.LoadOrStore(name, data)
	if loaded {
		return errors.Wrapf(errors.AlreadyExists, "Reconclier %s, already exists", name)
	}

	// initiate a new pipeline for reconcilation triggers
	data.pipeline = NewPipeline(m.ctx, crtl.Reconcile)

	// ensure triggering reconciliation of existing entries
	// separately for reconciliation by the controller
	go func() {
		keys, ok := m.existingKeys()
		if !ok {
			return
		}
		for _, key := range keys {
			err := data.pipeline.Enqueue(key)
			if err != nil {
				if m.ctx.Err() != nil {
					// the manager has ended, nothing is left
					// to replay into
					return
				}
				// unreachable while Enqueue fails only once the
				// manager's context has ended; kept for any other
				// failure
				log.Panicln("failed to enqueue an entry from existing in the queue", err)
			}
		}
	}()

	return nil
}

// existingKeys lists the keys Register replays into a new pipeline. It
// reports false if the listing failed because the manager's context has
// ended; any other failure of a KeyLister panics, as ReconcilerGetAllKeys
// does.
func (m *ManagerImpl) existingKeys() ([]any, bool) {
	lister, ok := m.parent.(KeyLister)
	if !ok {
		return m.parent.ReconcilerGetAllKeys(), true
	}
	keys, err := lister.ReconcilerListKeys(m.ctx)
	if err != nil {
		if m.ctx.Err() != nil {
			return nil, false
		}
		log.Panicf("got error while fetching all keys %s", err)
	}
	return keys, true
}
