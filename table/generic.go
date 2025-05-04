// Copyright © 2025 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"log"
	"reflect"

	"github.com/Prabhjot-Sethi/core/db"
	"github.com/Prabhjot-Sethi/core/errors"
	"github.com/Prabhjot-Sethi/core/reconciler"
)

// Generic table type providing common functions types to specific
// structures each table built using.
// ensure performing sanity checks and ensuring common
// functionality
type Table[K any, E any] struct {
	reconciler.ManagerImpl
	col db.StoreCollection
}

func (t *Table[K, E]) Initialize(col db.StoreCollection) error {
	if t.col != nil {
		return errors.Wrapf(errors.AlreadyExists, "Table is already initialized")
	}
	var key [0]K
	kType := reflect.TypeOf(key)
	if kType.Kind() != reflect.Pointer {
		kType = reflect.PointerTo(kType)
	}

	err := col.SetKeyType(kType)
	if err != nil {
		return err
	}

	err = col.Watch(context.Background(), nil, t.callback)
	if err != nil {
		return err
	}

	err = t.ManagerImpl.Initialize(context.Background(), t)
	if err != nil {
		return err
	}

	t.col = col
	return nil
}

func (t *Table[K, E]) callback(op string, wKey any) {
	t.NotifyCallback(wKey)
}

type keyOnly[K any] struct {
	Key K `bson:"_id,omitempty"`
}

func (t *Table[K, E]) ReconcilerGetAllKeys() []any {
	list := []keyOnly[K]{}
	keys := []any{}
	err := t.col.FindMany(context.Background(), nil, &list)
	if err != nil {
		log.Panicf("got error while fetching all keys %s", err)
	}
	for _, k := range list {
		keys = append(keys, k.Key)
	}
	return []any(keys)
}

// Inserts a new entry to the table
func (t *Table[K, E]) Insert(ctx context.Context, key K, entry E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.InsertOne(ctx, key, entry)
}

// Locates an entry in the table, inserts if it doesn't exists
// and updates the data if it already exists
func (t *Table[K, E]) Locate(ctx context.Context, key K, entry E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.UpdateOne(ctx, key, entry, true)
}

// Updates an existing entry
func (t *Table[K, E]) Update(ctx context.Context, key K, entry E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.UpdateOne(ctx, key, entry, false)
}

// Find an existing entry from the table
func (t *Table[K, E]) Find(ctx context.Context, key K, data E) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.FindOne(ctx, key, data)
}

// Delete a specific key from the table
func (t *Table[K, E]) DeleteKey(ctx context.Context, key K) error {
	if t.col == nil {
		return errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	return t.col.DeleteOne(ctx, key)
}
