// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"reflect"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
)

// Where selects rows by conditions, ANDed with each other and with the
// filter argument of FindManyWithOpts / DBFindManyWithOpts. It does not add
// a CachedTable's configured filter: a find returns the same rows whether or
// not a Where is present, and Raw(scope) narrows it to the table's scope.
// Where with no conditions is refused when the find runs.
func Where(conds ...Cond) FindOption {
	conds = append([]Cond(nil), conds...) // the caller's slice may change before the option is used
	return func(o *FindOptions) {
		o.whereGiven = true
		if len(conds) == 0 {
			o.whereEmpty = true
		}
		o.conds = append(o.conds, conds...)
	}
}

// Conds returns the conditions recorded by Where, for code that applies
// FindOptions itself; BuildFilter turns them into the filter a table sends.
func (o *FindOptions) Conds() []Cond {
	return append([]Cond(nil), o.conds...)
}

// findFilter returns the filter a find sends: the caller's filter as it is
// when no Where was given; otherwise the conditions ANDed with it and with
// any scope.
func findFilter[E any](o *FindOptions, filter any, scope ...any) (any, error) {
	if !o.whereGiven {
		return filter, nil
	}
	if o.whereEmpty {
		return nil, errors.Wrap(errors.InvalidArgument, "Where: no conditions")
	}
	extras := append([]any{filter}, scope...)
	f, err := buildFilter(reflect.TypeOf((*E)(nil)).Elem(), o.conds, extras...)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return filter, nil
	}
	return f, nil
}

// countWhere counts the rows holding conds, within scope when one is given.
func countWhere[E any](ctx context.Context, col db.StoreCollection, scope any, conds []Cond) (int64, error) {
	if col == nil {
		return 0, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	f, err := buildFilter(reflect.TypeOf((*E)(nil)).Elem(), conds, scope)
	if err != nil {
		return 0, err
	}
	var filter any
	if f != nil {
		filter = f
	}
	n, err := col.Count(ctx, filter)
	if err != nil {
		return 0, preserveErrClass(err, "failed to count entries")
	}
	return n, nil
}

// deleteWhere deletes the rows holding conds, within scope when one is given;
// it refuses an empty condition list.
func deleteWhere[E any](ctx context.Context, col db.StoreCollection, scope any, conds []Cond) (int64, error) {
	if col == nil {
		return 0, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	if len(conds) == 0 {
		return 0, errors.Wrap(errors.InvalidArgument,
			"DeleteWhere: no conditions; to delete every row, use DeleteByFilter(ctx, bson.D{})")
	}
	f, err := buildFilter(reflect.TypeOf((*E)(nil)).Elem(), conds, scope)
	if err != nil {
		return 0, err
	}
	n, err := col.DeleteMany(ctx, f)
	if err != nil {
		if errors.IsNotFound(err) {
			// "delete what matches" has nothing to report when nothing does
			return 0, nil
		}
		return 0, preserveErrClass(err, "failed to delete entries")
	}
	return n, nil
}

// CountWhere counts the rows that hold every condition; with none, every row.
func (t *Table[K, E]) CountWhere(ctx context.Context, conds ...Cond) (int64, error) {
	return countWhere[E](ctx, t.col, nil, conds)
}

// DeleteWhere deletes the rows that hold every condition and returns how
// many. Nothing matching is (0, nil). With no conditions it is refused. A
// delete of several rows is not atomic: on an error the count is 0, and
// some matching rows may already be deleted.
func (t *Table[K, E]) DeleteWhere(ctx context.Context, conds ...Cond) (int64, error) {
	return deleteWhere[E](ctx, t.col, nil, conds)
}

// CountWhere counts the rows of this table's scope (its configured filter)
// that hold every condition; with none, every row in scope.
func (t *CachedTable[K, E]) CountWhere(ctx context.Context, conds ...Cond) (int64, error) {
	return countWhere[E](ctx, t.col, t.filter, conds)
}

// DeleteWhere is Table.DeleteWhere within this table's scope (its
// configured filter).
func (t *CachedTable[K, E]) DeleteWhere(ctx context.Context, conds ...Cond) (int64, error) {
	return deleteWhere[E](ctx, t.col, t.filter, conds)
}
