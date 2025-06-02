// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
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
			log.Panicln("Failed to enqueue an entry for reconciliation", name, err)
		}
		return true
	})
}

// Initialize the manager with context and relevant collection to work with
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
		keys := m.parent.ReconcilerGetAllKeys()
		for _, key := range keys {
			err := data.pipeline.Enqueue(key)
			if err != nil {
				log.Panicln("failed to enqueue an entry from existing in the queue", err)
			}
		}
	}()

	return nil
}
