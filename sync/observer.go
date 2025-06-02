// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package sync

import (
	"sync"

	"github.com/go-core-stack/core/reconciler"
)

type observerCountKey struct {
	ExtKey any `bson:"_id.extKey,omitempty"`
}

type observerTable struct {
	reconciler.ManagerImpl
	mu        sync.RWMutex
	providers map[any]struct{}
}

func (o *observerTable) getProviderList() []any {
	list := []any{}
	func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		for k := range o.providers {
			list = append(list, k)
		}
	}()
	return list
}

func (o *observerTable) isProviderAvailable(key any) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.providers[key]
	return ok
}

func (o *observerTable) deleteProvider(key any) {
	var ok bool
	func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		_, ok = o.providers[key]
		if ok {
			delete(o.providers, key)
		}
	}()
	if ok {
		// since the observer is removed trigger an update
		// for controllers
		o.NotifyCallback(key)
	}
}

func (o *observerTable) insertProvider(key any) {
	var ok bool
	func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		_, ok = o.providers[key]
		if !ok {
			o.providers[key] = struct{}{}
		}
	}()
	if !ok {
		// Notify an insert of new provider to providers
		o.NotifyCallback(key)
	}
}

func (o *observerTable) ReconcilerGetAllKeys() []any {
	return o.getProviderList()
}
