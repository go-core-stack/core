// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"reflect"
	"strconv"
	"strings"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// fieldInfo describes one bson path of an entry type, as the encoder writes
// it. It is what conditions, increments and unsets are checked against.
type fieldInfo struct {
	// typ is the field's Go type; elem is typ with one pointer removed.
	typ  reflect.Type
	elem reflect.Type

	// leaf is false for a nested struct the index walked into.
	leaf bool

	// bsonType is the BSON type the encoder writes for a value of elem.
	bsonType bson.Type

	// scalar fields hold a single BSON value that can be compared,
	// ranged over or zero-tested: booleans, numbers, strings, dates,
	// object ids, timestamps and decimals.
	scalar bool

	// ordered scalars can be compared with ranges.
	ordered bool

	// numeric scalars can be incremented.
	numeric bool

	// hasMap is set for a field whose value can contain a map anywhere: its
	// encoded key order is not stable, so it cannot be compared.
	hasMap bool

	// opaque is set for a field whose type encodes itself, or holds a type
	// that does. What it stores need not follow its Go shape, so typed
	// conditions and increments refuse it; Raw reaches it.
	opaque bool
}

// fieldIndex maps each bson path of an entry type ("spec.owner") to what the
// encoder writes there. It is built once per type and shared, so a table and
// BuildFilter for the same type agree. It never fails: a field it cannot
// describe is simply absent, and refused only when a condition names it.
type fieldIndex map[string]*fieldInfo

// rootOpaque is the index key that marks an entry type stored by its own
// encoder; no bson path can contain it.
const rootOpaque = "\x00entry encodes itself"

// maxFieldDepth bounds how deep the index walks nested structs, so a
// recursive type is indexed to a fixed depth rather than looping. A path
// deeper than this is unknown to conditions; Raw still reaches it.
const maxFieldDepth = 8

var (
	defaultRegistry = bson.NewRegistry()

	// plainStructEncoder is the encoder the registry uses for an ordinary
	// struct; a struct type with any other encoder (bson.Timestamp,
	// bson.Decimal128, time.Time, ...) is written as a value, not walked.
	plainStructEncoder = func() reflect.Type {
		enc, _ := defaultRegistry.LookupEncoder(reflect.TypeOf(struct{ X int }{}))
		return reflect.TypeOf(enc)
	}()

	bsonMarshalerType      = reflect.TypeOf((*bson.Marshaler)(nil)).Elem()
	bsonValueMarshalerType = reflect.TypeOf((*bson.ValueMarshaler)(nil)).Elem()

	fieldIndexes sync.Map // reflect.Type -> fieldIndex
)

// indexFor returns the field index of entry type t, building it on first use.
func indexFor(t reflect.Type) fieldIndex {
	if idx, ok := fieldIndexes.Load(t); ok {
		return idx.(fieldIndex)
	}
	idx := fieldIndex{}
	switch {
	case walkable(t):
		idx.walk(t, "", 0)
	case t.Kind() == reflect.Struct || customEncoded(t):
		// the entry is stored as its own encoder, or the registry, writes
		// it, not by its Go fields: no typed path can follow it
		idx[rootOpaque] = &fieldInfo{typ: t, elem: t, opaque: true}
	}
	actual, _ := fieldIndexes.LoadOrStore(t, idx)
	return actual.(fieldIndex)
}

// bsonField reads a struct field's bson name and options the way the
// driver's default tag parser does: the bson tag, or a bare tag with no key;
// the lower-cased field name when the tag gives none; and options recognised
// in every part of the tag, the first included.
func bsonField(f reflect.StructField) (name string, inline, skip bool) {
	name = strings.ToLower(f.Name)
	tag, ok := f.Tag.Lookup("bson")
	if !ok && !strings.Contains(string(f.Tag), ":") && len(f.Tag) > 0 {
		tag = string(f.Tag)
	}
	if tag == "-" {
		return "", false, true
	}
	for i, part := range strings.Split(tag, ",") {
		if i == 0 && part != "" {
			name = part
		}
		if part == "inline" {
			inline = true
		}
	}
	return name, inline, false
}

// levelField is one field the encoder writes at a struct level: a direct
// field, or one lifted from an inlined struct. inlineMap marks an inlined map,
// which has no fixed names and is kept only so a set one can be refused.
type levelField struct {
	name      string
	field     reflect.StructField
	index     []int // field indexes from the level's struct to this field
	inlineMap bool
}

// levelFields resolves the fields the encoder writes at one struct level, as
// it does: inlined structs are flattened into the level, and when two fields
// share a name the one inlined fewer times wins; two at the same depth cancel
// out, as they make the driver refuse the struct. Only winners are returned,
// so a field hidden by an outer one, and everything beneath it, is never seen.
func levelFields(t reflect.Type) []levelField {
	type candidate struct {
		levelField
		depth int
	}
	var all []candidate
	var collect func(t reflect.Type, prefix []int, depth int)
	collect = func(t reflect.Type, prefix []int, depth int) {
		if depth > maxFieldDepth {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue // the encoder skips unexported fields, embedded ones included
			}
			name, inline, skip := bsonField(f)
			if skip {
				continue
			}
			index := append(append([]int(nil), prefix...), i)
			if inline {
				switch elem := deref(f.Type); {
				case elem.Kind() == reflect.Struct:
					// the driver flattens an inlined struct by its fields,
					// even one with its own encoder or a registry codec
					collect(elem, index, depth+1)
				case elem.Kind() == reflect.Map:
					// a unique name keeps it out of the name resolution
					all = append(all, candidate{levelField{name: "\x00inline-map-" + strconv.Itoa(len(all)), field: f, index: index, inlineMap: true}, depth})
				}
				continue
			}
			all = append(all, candidate{levelField{name: name, field: f, index: index}, depth})
		}
	}
	collect(t, nil, 0)

	// per name, the shallowest candidates compete; a single one wins, two or
	// more at that depth cancel out
	minDepth := map[string]int{}
	for _, c := range all {
		if d, seen := minDepth[c.name]; !seen || c.depth < d {
			minDepth[c.name] = c.depth
		}
	}
	best := map[string]int{} // name -> position in all, or -1 when tied
	for i, c := range all {
		if c.depth != minDepth[c.name] {
			continue
		}
		if _, seen := best[c.name]; seen {
			best[c.name] = -1
		} else {
			best[c.name] = i
		}
	}
	var out []levelField
	for i, c := range all {
		if best[c.name] == i {
			out = append(out, c.levelField)
		}
	}
	return out
}

// deref removes one level of pointer from t, as the encoder does for a field.
func deref(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

// customEncoded reports whether t encodes itself, so its bson shape is not
// its Go shape.
func customEncoded(t reflect.Type) bool {
	return t.Implements(bsonMarshalerType) || t.Implements(bsonValueMarshalerType) ||
		reflect.PointerTo(t).Implements(bsonMarshalerType) || reflect.PointerTo(t).Implements(bsonValueMarshalerType)
}

// walkable reports whether the encoder writes t as a document of its fields,
// so the index and Match descend into it as dotted paths.
func walkable(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || customEncoded(t) {
		return false
	}
	enc, err := defaultRegistry.LookupEncoder(t)
	return err == nil && reflect.TypeOf(enc) == plainStructEncoder
}

// bsonTypeOf returns the BSON type the encoder writes for a value of t: a
// zero value, or an empty slice or map, so a collection reports its own type
// rather than null.
func bsonTypeOf(t reflect.Type) (bt bson.Type) {
	v := reflect.Zero(t)
	switch t.Kind() {
	case reflect.Slice:
		v = reflect.MakeSlice(t, 0, 0)
	case reflect.Map:
		v = reflect.MakeMap(t)
	}
	defer func() { _ = recover() }() // a type that cannot encode its zero value is left unclassified
	bt, _, _ = bson.MarshalValue(v.Interface())
	return bt
}

// containsCustom reports whether a value of type t is, or can hold, a type
// that encodes itself: directly, as a slice, array or pointer element, or in
// a field of a struct the encoder walks.
func containsCustom(t reflect.Type, depth int) bool {
	if depth > maxFieldDepth {
		return false
	}
	if customEncoded(t) {
		return true
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return containsCustom(t.Elem(), depth+1)
	case reflect.Struct:
		if !walkable(t) {
			return false
		}
		for _, lf := range levelFields(t) {
			if containsCustom(lf.field.Type, depth+1) {
				return true
			}
		}
	}
	return false
}

// isNumericBSON reports whether values of type bt compare and increment as
// numbers; MongoDB compares the numeric types with each other by value.
func isNumericBSON(bt bson.Type) bool {
	return bt == bson.TypeDouble || bt == bson.TypeInt32 || bt == bson.TypeInt64 || bt == bson.TypeDecimal128
}

// classify fills in what a leaf of type elem is, from the BSON type the
// encoder writes for it.
func classify(info *fieldInfo, elem reflect.Type) {
	info.hasMap = containsMap(elem, 0)
	if containsCustom(elem, 0) {
		info.opaque = true
		return
	}
	bt := bsonTypeOf(elem)
	info.bsonType = bt
	switch {
	case bt == bson.TypeBoolean:
		info.scalar = true
	case bt == bson.TypeString, bt == bson.TypeDateTime, bt == bson.TypeObjectID, bt == bson.TypeTimestamp:
		info.scalar, info.ordered = true, true
	case isNumericBSON(bt):
		info.scalar, info.ordered, info.numeric = true, true, true
	}
}

// containsMap reports whether a value of type t can hold a map anywhere:
// directly, as a slice, array or pointer element, or in a field of a struct
// the encoder walks. Types that encode themselves are not looked into.
func containsMap(t reflect.Type, depth int) bool {
	if depth > maxFieldDepth {
		return false
	}
	switch t.Kind() {
	case reflect.Map:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return containsMap(t.Elem(), depth+1)
	case reflect.Struct:
		if !walkable(t) {
			return false
		}
		for _, lf := range levelFields(t) {
			if lf.inlineMap || containsMap(lf.field.Type, depth+1) {
				return true
			}
		}
	}
	return false
}

// maxValueDepth bounds how far valueHasMap follows a value. Every pointer,
// interface, element and field is one step, so a bson.D nested in a bson.D
// costs three; past the bound a value is reported as unchecked.
const maxValueDepth = 64

// containsInterface reports whether a value of type t can hold an interface
// anywhere, the only place a map can hide from containsMap.
func containsInterface(t reflect.Type, depth int) bool {
	if depth > maxFieldDepth {
		return true
	}
	switch t.Kind() {
	case reflect.Interface:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return containsInterface(t.Elem(), depth+1)
	case reflect.Struct:
		if !walkable(t) {
			return false
		}
		for _, lf := range levelFields(t) {
			if containsInterface(lf.field.Type, depth+1) {
				return true
			}
		}
	}
	return false
}

// valueHasMap reports whether the value v holds a map anywhere, following the
// interfaces a check of its static type cannot see into: inside a list, a
// bson.D, or a struct the encoder walks. A value it cannot look into, one
// that encodes itself or one nested past maxValueDepth, is reported as
// holding one, so nothing goes unchecked.
func valueHasMap(v reflect.Value, depth int) bool {
	if !v.IsValid() {
		return false
	}
	if depth > maxValueDepth || customEncoded(v.Type()) {
		return true
	}
	switch v.Kind() {
	case reflect.Map:
		return true
	case reflect.Interface, reflect.Pointer:
		return !v.IsNil() && valueHasMap(v.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if valueHasMap(v.Index(i), depth+1) {
				return true
			}
		}
	case reflect.Struct:
		if !walkable(v.Type()) {
			return false
		}
		for _, lf := range levelFields(v.Type()) {
			fv, ok := fieldByIndex(v, lf.index)
			if !ok {
				continue
			}
			if valueHasMap(fv, depth+1) {
				return true
			}
		}
	}
	return false
}

// walk records every path the encoder writes for struct type t under prefix,
// descending into nested structs it writes as documents.
func (idx fieldIndex) walk(t reflect.Type, prefix string, depth int) {
	if depth >= maxFieldDepth {
		return
	}
	for _, lf := range levelFields(t) {
		if lf.inlineMap {
			continue
		}
		path := prefix + lf.name
		elem := deref(lf.field.Type)
		info := &fieldInfo{typ: lf.field.Type, elem: elem, leaf: true}
		if walkable(elem) {
			info.leaf = false
			idx.walk(elem, path+".", depth+1)
		} else {
			classify(info, elem)
		}
		idx[path] = info
	}
}

// overlaps reports whether two bson paths touch the same data: equal, or one
// the parent of the other.
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+".") || strings.HasPrefix(b, a+".")
}
