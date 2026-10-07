// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"reflect"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
)

// UpdateOption configures UpdateWithOpts.
type UpdateOption func(*updateOptions)

type updateOptions struct {
	conds   []Cond
	ifGiven bool
	ifEmpty bool

	increments []increment
	unset      []string
}

type increment struct {
	pairs []setField
	err   error
}

// If makes the update conditional: it applies only if the row with the key
// holds every condition. Several If options are ANDed. If with no conditions
// is refused. On a CachedTable the table's configured filter is ANDed in too.
func If(conds ...Cond) UpdateOption {
	conds = append([]Cond(nil), conds...) // the caller's slice may change before the option is used
	return func(o *updateOptions) {
		o.ifGiven = true
		if len(conds) == 0 {
			o.ifEmpty = true
		}
		o.conds = append(o.conds, conds...)
	}
}

// WithIncrement adds each set field of fields to the stored value, in the
// same atomic write; a negative value decrements. Fields must be numeric.
// fields may be the table's entry type or a separate struct whose bson paths
// name numeric fields of it, as for Match.
func WithIncrement[F any](fields *F) UpdateOption {
	inc := increment{}
	if fields == nil {
		inc.err = errors.Wrap(errors.InvalidArgument, "WithIncrement: nil entry")
	} else if v := reflect.ValueOf(fields).Elem(); v.Kind() != reflect.Struct {
		inc.err = errors.Wrapf(errors.InvalidArgument, "WithIncrement: %v is not a struct", v.Type())
	} else {
		inc.pairs, inc.err = setFieldsOf(v, "WithIncrement")
		if inc.err == nil && len(inc.pairs) == 0 {
			inc.err = errors.Wrap(errors.InvalidArgument, "WithIncrement: no set field, which would add nothing")
		}
	}
	return func(o *updateOptions) { o.increments = append(o.increments, inc) }
}

// WithUnset removes the fields at paths, in the same atomic write.
func WithUnset(paths ...string) UpdateOption {
	paths = append([]string(nil), paths...) // the caller's slice may change before the option is used
	return func(o *updateOptions) { o.unset = append(o.unset, paths...) }
}

// conditionalUpdate is UpdateWithOpts for both table types. scope is a
// CachedTable's configured filter, ANDed into the conditions when If is
// given; nil for Table.
func conditionalUpdate[K any, E any](ctx context.Context, col db.StoreCollection, scope any, key *K, entry *E, opts []UpdateOption) (bool, error) {
	if col == nil {
		return false, errors.Wrapf(errors.InvalidArgument, "Table not initialized")
	}
	if key == nil {
		return false, errors.Wrapf(errors.InvalidArgument, "UpdateWithOpts: nil key")
	}
	o := &updateOptions{}
	for _, opt := range opts {
		if opt == nil {
			// refused rather than skipped: a nil If would otherwise make a
			// guarded update unconditional
			return false, errors.Wrap(errors.InvalidArgument, "UpdateWithOpts: nil option")
		}
		opt(o)
	}
	entryType := reflect.TypeOf((*E)(nil)).Elem()
	idx := indexFor(entryType)

	// What entry writes: exactly what Update writes.
	var set bson.D
	if entry != nil {
		raw, err := bson.Marshal(entry)
		if err != nil {
			return false, errors.WrapErrf(errors.InvalidArgument, err, "UpdateWithOpts: encoding entry")
		}
		if err := bson.Unmarshal(raw, &set); err != nil {
			return false, errors.WrapErrf(errors.InvalidArgument, err, "UpdateWithOpts: encoding entry")
		}
	}

	type written struct{ path, by string }
	var writes []written
	for _, e := range set {
		writes = append(writes, written{e.Key, "the entry"})
	}

	inc := bson.D{}
	for _, in := range o.increments {
		if in.err != nil {
			return false, in.err
		}
		for _, p := range in.pairs {
			info, err := typedField(idx, p.path, "WithIncrement")
			if err != nil {
				return false, err
			}
			if containsCustom(p.goType, 0) {
				return false, errors.Wrapf(errors.InvalidArgument,
					"WithIncrement on %q: a value of type %v encodes itself; use Raw", p.path, p.goType)
			}
			if !info.numeric || !isNumericBSON(p.value.Type) {
				return false, errors.Wrapf(errors.InvalidArgument, "WithIncrement on %q: not a numeric field and value", p.path)
			}
			// converted to the field's own type, so an increment never turns
			// an integer field into a double, or overflows it
			v, err := convertValue(p.goValue, info.elem, p.path)
			if err != nil {
				return false, err
			}
			inc = append(inc, bson.E{Key: p.path, Value: v})
			writes = append(writes, written{p.path, "the increment"})
		}
	}
	for _, path := range o.unset {
		if path == "_id" || strings.HasPrefix(path, "_id.") {
			return false, errors.Wrapf(errors.InvalidArgument, "WithUnset on %q: the key cannot be changed", path)
		}
		if _, err := fieldFor(idx, path); err != nil {
			return false, err
		}
		writes = append(writes, written{path, "the unset list"})
	}

	// One path, or a path and its parent, written twice is refused here
	// rather than by the server.
	for i := range writes {
		for j := i + 1; j < len(writes); j++ {
			if overlaps(writes[i].path, writes[j].path) {
				return false, errors.Wrapf(errors.InvalidArgument,
					"UpdateWithOpts: %q in %s overlaps %q in %s", writes[i].path, writes[i].by, writes[j].path, writes[j].by)
			}
		}
	}
	if len(writes) == 0 {
		return false, errors.Wrap(errors.InvalidArgument, "UpdateWithOpts: nothing to write")
	}

	spec := db.UpdateSpec{Unset: o.unset}
	if len(set) > 0 {
		spec.Set = set
	}
	if len(inc) > 0 {
		spec.Inc = inc
	}
	if o.ifGiven {
		if o.ifEmpty {
			return false, errors.Wrap(errors.InvalidArgument, "If: no conditions")
		}
		match, err := buildFilter(entryType, o.conds, scope)
		if err != nil {
			return false, err
		}
		spec.Match = match
	}

	applied, err := col.UpdateOneWithSpec(ctx, key, spec)
	if err != nil {
		return false, preserveErrClass(err, "failed to update entry with key %v", key)
	}
	return applied, nil
}

// UpdateWithOpts writes entry as Update does, together with the changes and
// conditions in opts, in one atomic operation on one row. It never inserts.
//
// It returns true when the row matched every condition and the write was
// applied; a row matched but left unchanged counts as applied. It returns
// false when no row with this key held the conditions: the row may not hold
// them, or may not exist. With no If option, a missing row is a NotFound
// error, as for Update.
//
// entry is encoded as Update encodes it, so a sparse entry (only a few
// fields) needs omitempty on every field and pointers for nested structs. It
// may be nil when the call only increments or unsets.
//
// After an Unavailable error, a cancelled context or a client shutdown the
// write may or may not have been applied, and a retry can return false for
// its own earlier write: false means "the row does not hold the conditions
// now", not "someone else won".
func (t *Table[K, E]) UpdateWithOpts(ctx context.Context, key *K, entry *E, opts ...UpdateOption) (bool, error) {
	return conditionalUpdate(ctx, t.col, nil, key, entry, opts)
}

// UpdateWithOpts is Table.UpdateWithOpts for a cached table. When If is given
// the table's configured filter is ANDed in, so a table scoped to part of a
// shared collection never updates a row outside its scope. The cache follows
// the write through the change stream, as for Update.
func (t *CachedTable[K, E]) UpdateWithOpts(ctx context.Context, key *K, entry *E, opts ...UpdateOption) (bool, error) {
	return conditionalUpdate(ctx, t.col, t.filter, key, entry, opts)
}
