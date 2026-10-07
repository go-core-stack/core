// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"math"
	"reflect"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/errors"
)

/*
Conditions

A Cond is one condition on a table's entry type. The same conditions select
rows (Where in FindManyWithOpts, CountWhere, DeleteWhere) and guard a
conditional write (If in UpdateWithOpts), so a find of candidates and the
update of each one test the same thing:

	claimable := []table.Cond{
	    table.In("status", "queued", "requeued"),
	    table.LessEq("notBefore", now),
	}
	jobs, err := jobsTable.FindManyWithOpts(ctx, nil, table.Where(claimable...))
	...
	won, err := jobsTable.UpdateWithOpts(ctx, key, &claim, table.If(claimable...))

A Cond copies what it is given when it is built. It is checked against the
entry type's fields when the operation runs, and a problem is reported as
InvalidArgument then, before anything is sent: an unknown path, a value that
does not fit the field, a condition on a field it does not apply to.

All conditions in a call are ANDed. A path may appear in at most one
equality-type condition (Match, MatchZero, In, NotIn), and not also in a
range; several ranges on one path combine into a window.
*/
type Cond struct {
	kind condKind

	pairs  []setField // Match
	paths  []string   // MatchZero
	path   string     // In, NotIn, ranges
	op     string     // ranges: $lt, $lte, $gt, $gte
	values []any      // In, NotIn, ranges (one value)
	raw    any        // Raw

	err error // a problem found while building, reported when the operation runs
}

type condKind int

const (
	// condInvalid is the zero value, so a Cond not made by a constructor
	// (Cond{}, an unset variable or slice slot) is refused, never read as an
	// empty Match
	condInvalid condKind = iota
	condMatch
	condZero
	condIn
	condNotIn
	condRange
	condRaw
)

// setField is one set field of a struct value: its bson path, its value as
// the encoder writes it (snapshotted when the condition is built), and its Go
// type.
type setField struct {
	path    string
	value   bson.RawValue
	goValue any // the value itself, for converting an increment to its field's type
	goType  reflect.Type
	hasMap  bool
}

// Match requires every set field of fields to equal the stored value. A field
// is set when it is a non-nil pointer, or a non-pointer whose value is not
// its type's zero value; an empty non-nil slice counts as set. Nested structs
// are compared field by field as dotted paths.
//
// fields may be the table's entry type or any struct whose bson paths name
// fields of it: a separate filter struct, with tags such as `bson:"status"`
// or `bson:"spec.owner"`. Each path must exist on the entry, and each value
// must encode to a type the entry field holds. A Match with no set field, or
// with a set field that contains a map, is refused when the operation runs.
//
// To require a zero value, use a pointer to it, or MatchZero.
func Match[F any](fields *F) Cond {
	c := Cond{kind: condMatch}
	if fields == nil {
		c.err = errors.Wrap(errors.InvalidArgument, "Match: nil entry")
		return c
	}
	v := reflect.ValueOf(fields).Elem()
	if v.Kind() != reflect.Struct {
		c.err = errors.Wrapf(errors.InvalidArgument, "Match: %v is not a struct", v.Type())
		return c
	}
	c.pairs, c.err = setFieldsOf(v, "Match")
	if c.err == nil && len(c.pairs) == 0 {
		c.err = errors.Wrap(errors.InvalidArgument, "Match: no set field, which would match every row")
	}
	return c
}

// MatchZero requires each field to be its type's zero value, null or absent.
// All three are needed: omitempty stores a zero as absent, and a pointer
// without omitempty stores nil as null. Scalar fields only.
func MatchZero(paths ...string) Cond {
	c := Cond{kind: condZero, paths: append([]string(nil), paths...)}
	if len(paths) == 0 {
		c.err = errors.Wrap(errors.InvalidArgument, "MatchZero: no path")
	}
	return c
}

// In requires the field to equal one of values. Each value is converted to
// the field's type, so an untyped constant or a plain string works for a
// field of a named or wider type. A zero value among them also matches null
// and absent. Scalar fields only.
func In(path string, values ...any) Cond {
	return valuesCond(condIn, "In", path, values)
}

// NotIn requires the field to equal none of values. It matches a row where
// the field is absent, unless the zero value is among values. Scalar fields
// only.
func NotIn(path string, values ...any) Cond {
	return valuesCond(condNotIn, "NotIn", path, values)
}

// Less requires the stored value to be less than v. Ranges apply to ordered
// fields only (numbers, strings, time.Time and bson dates, object ids,
// timestamps and decimals, or pointers to them; not types with their own
// encoder), and an absent or null field matches no range.
func Less(path string, v any) Cond { return rangeCond(path, "$lt", v) }

// LessEq requires the stored value to be at most v.
func LessEq(path string, v any) Cond { return rangeCond(path, "$lte", v) }

// Greater requires the stored value to be more than v.
func Greater(path string, v any) Cond { return rangeCond(path, "$gt", v) }

// GreaterEq requires the stored value to be at least v.
func GreaterEq(path string, v any) Cond { return rangeCond(path, "$gte", v) }

// rangeCond builds a range condition with the given comparison operator.
func rangeCond(path, op string, v any) Cond {
	c := valuesCond(condRange, "range", path, []any{v})
	c.op = op
	return c
}

// valuesCond builds a condition on one path, copying its values now: a
// pointer is dereferenced and its value copied, so changing the variable
// later does not change the condition.
func valuesCond(kind condKind, name, path string, values []any) Cond {
	c := Cond{kind: kind, path: path}
	for _, v := range values {
		cv, err := ownValue(v)
		if err != nil {
			c.err = errors.Wrapf(errors.InvalidArgument, "%s on %q: %s", name, path, err)
			return c
		}
		c.values = append(c.values, cv)
	}
	return c
}

// ownValue returns a copy of v that later changes to the caller's variables
// cannot reach.
func ownValue(v any) (any, error) {
	if v == nil {
		return nil, errors.New("nil value")
	}
	rv := reflect.ValueOf(v)
	for steps := 0; rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface; steps++ {
		if rv.IsNil() {
			return nil, errors.New("nil value")
		}
		if steps > maxValueDepth {
			return nil, errors.New("the value refers to itself or nests too deep")
		}
		rv = rv.Elem()
	}
	if containsCustom(rv.Type(), 0) {
		return nil, errors.New("a value of type " + rv.Type().String() + " encodes itself; use Raw for it")
	}
	return copyValue(rv), nil
}

// Raw ANDs a filter document in as it is, unchecked. It is the escape hatch
// for what the typed conditions cannot say: $or, $expr, $elemMatch, regular
// expressions. The document is encoded when the condition is built, so
// clearing or changing it afterwards cannot widen the condition. An empty
// document is refused.
func Raw(filter any) Cond {
	c := Cond{kind: condRaw}
	if filter == nil || (reflect.ValueOf(filter).Kind() == reflect.Pointer && reflect.ValueOf(filter).IsNil()) {
		c.err = errors.Wrap(errors.InvalidArgument, "Raw: nil filter document")
		return c
	}
	doc, err := encodeDocument(filter)
	if err != nil {
		c.err = errors.Wrapf(errors.InvalidArgument, "Raw: %s", err)
		return c
	}
	// judged by what will be sent: a struct whose fields are all omitted
	// encodes to {}, which would match every row
	if emptyEncoded(doc) {
		c.err = errors.Wrap(errors.InvalidArgument, "Raw: the filter encodes to an empty document")
		return c
	}
	// a copy: a bson.Raw argument is the caller's own storage, and the
	// condition must not change when it does
	c.raw = append(bson.Raw(nil), doc...)
	return c
}

// encodeDocument encodes a filter document exactly as it was given, as the
// driver would: what is judged from these bytes is what is sent, because the
// bytes are what is sent.
func encodeDocument(f any) ([]byte, error) {
	if raw, ok := f.(bson.Raw); ok {
		return raw, nil
	}
	return bson.Marshal(f)
}

// encodesToNothing reports whether a filter element encodes to a document
// with no elements.
func encodesToNothing(el any) (bool, error) {
	doc, err := encodeDocument(el)
	if err != nil {
		return false, err
	}
	return emptyEncoded(doc), nil
}

// emptyEncoded reports whether an encoded document has no elements.
func emptyEncoded(doc []byte) bool {
	elems, err := bson.Raw(doc).Elements()
	return err == nil && len(elems) == 0
}

// setFieldsOf returns the set fields of a struct value as bson paths, each
// value encoded now, so later changes to the caller's struct, at any depth,
// cannot reach the condition. It refuses what cannot be expressed as
// conditions: a set inlined map, and a set nested struct with no field.
func setFieldsOf(v reflect.Value, name string) ([]setField, error) {
	pairs, err := collectSetFields(v, "", 0)
	if err != nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "%s: %s", name, err)
	}
	return pairs, nil
}

// collectSetFields walks the fields the encoder writes at one struct level
// (see levelFields) and returns the set ones under prefix, descending into
// nested structs.
func collectSetFields(v reflect.Value, prefix string, depth int) ([]setField, error) {
	if depth >= maxFieldDepth {
		return nil, nil
	}
	var out []setField
	for _, lf := range levelFields(v.Type()) {
		fv, reachable := fieldByIndex(v, lf.index)
		if !reachable {
			continue // inside a nil inlined pointer: not set
		}
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		} else if fv.Kind() == reflect.Slice {
			if fv.IsNil() {
				continue
			}
		} else if fv.IsZero() {
			continue
		}

		path := prefix + lf.name
		switch {
		case lf.inlineMap:
			return nil, errors.New("a set inlined map has no fixed field names to compare")
		case walkable(fv.Type()):
			nested, err := collectSetFields(fv, path+".", depth+1)
			if err != nil {
				return nil, err
			}
			if len(nested) == 0 {
				return nil, errors.New("set field " + path + " has no field that can be compared")
			}
			out = append(out, nested...)
		default:
			t, data, err := bson.MarshalValue(fv.Interface())
			if err != nil {
				return nil, errors.New("set field " + path + " cannot be encoded: " + err.Error())
			}
			// an interface field is judged by the value it holds: its type
			// for fitting, and whether that contains a map
			dynamic := fv.Type()
			if fv.Kind() == reflect.Interface && !fv.IsNil() {
				dynamic = fv.Elem().Type()
			}
			out = append(out, setField{
				path:    path,
				value:   bson.RawValue{Type: t, Value: data},
				goValue: snapshotValue(fv),
				goType:  dynamic,
				hasMap:  containsMap(dynamic, 0) || (containsInterface(fv.Type(), 0) && valueHasMap(fv, 0)),
			})
		}
	}
	return out, nil
}

// fieldByIndex follows a chain of field indexes through inlined structs,
// reporting false when it meets a nil pointer on the way.
func fieldByIndex(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v, true
}

// snapshotValue returns what v holds, through pointers and interfaces, as an
// independent copy, so nothing the caller changes later reaches it; nil when
// it holds nothing.
func snapshotValue(v reflect.Value) any {
	for steps := 0; v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface; steps++ {
		if v.IsNil() || steps > maxValueDepth {
			return nil // nothing, or a value that refers to itself: refused as not numeric where it is used
		}
		v = v.Elem()
	}
	return copyValue(v)
}

// copyValue returns an independent copy of a field's value, so a Cond does
// not change when the struct it was built from does.
func copyValue(v reflect.Value) any {
	if v.Kind() == reflect.Slice {
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(c, v)
		return c.Interface()
	}
	return v.Interface()
}

// typedField looks a path up for a typed condition or increment, refusing a
// field whose type encodes itself: what it stores need not follow its Go
// shape, so only Raw can name it reliably.
func typedField(idx fieldIndex, path, name string) (*fieldInfo, error) {
	info, err := fieldFor(idx, path)
	if err != nil {
		return nil, err
	}
	if info.opaque {
		return nil, errors.Wrapf(errors.InvalidArgument,
			"%s on %q: the field's type encodes itself, so typed conditions cannot follow what it stores; use Raw", name, path)
	}
	return info, nil
}

// fieldFor looks a path up in the index.
func fieldFor(idx fieldIndex, path string) (*fieldInfo, error) {
	if root, ok := idx[rootOpaque]; ok {
		return nil, errors.Wrapf(errors.InvalidArgument,
			"field %q: the entry type %v encodes itself, so typed conditions, increments and unsets cannot follow what it stores; use Raw", path, root.typ)
	}
	info, ok := idx[path]
	if !ok {
		return nil, errors.Wrapf(errors.InvalidArgument, "unknown field path %q", path)
	}
	return info, nil
}

// element turns a condition into one filter document, checked against the
// entry type's fields.
func (c Cond) element(entry reflect.Type, idx fieldIndex) (any, error) {
	if c.err != nil {
		return nil, c.err
	}
	switch c.kind {
	case condMatch:
		doc := bson.D{}
		for _, p := range c.pairs {
			info, err := typedField(idx, p.path, "Match")
			if err != nil {
				return nil, err
			}
			if containsCustom(p.goType, 0) {
				return nil, errors.Wrapf(errors.InvalidArgument,
					"Match on %q: a value of type %v encodes itself; use Raw", p.path, p.goType)
			}
			if p.hasMap || info.hasMap {
				return nil, errors.Wrapf(errors.InvalidArgument, "Match on %q: it contains a map, whose encoded key order is not stable, or a value it cannot look into", p.path)
			}
			if err := fitsField(p, info); err != nil {
				return nil, err
			}
			doc = append(doc, bson.E{Key: p.path, Value: p.value})
		}
		return doc, nil

	case condZero:
		doc := bson.D{}
		for _, path := range c.paths {
			info, err := scalarField(idx, path, "MatchZero")
			if err != nil {
				return nil, err
			}
			doc = append(doc, bson.E{Key: path, Value: bson.D{{Key: "$in", Value: bson.A{reflect.Zero(info.elem).Interface(), nil}}}})
		}
		return doc, nil

	case condIn, condNotIn:
		name, op := "In", "$in"
		if c.kind == condNotIn {
			name, op = "NotIn", "$nin"
		}
		info, err := scalarField(idx, c.path, name)
		if err != nil {
			return nil, err
		}
		if len(c.values) == 0 {
			return nil, errors.Wrapf(errors.InvalidArgument, "%s on %q with no values", name, c.path)
		}
		arr := bson.A{}
		hasZero := false
		for _, v := range c.values {
			cv, err := convertValue(v, info.elem, c.path)
			if err != nil {
				return nil, err
			}
			if reflect.ValueOf(cv).IsZero() {
				hasZero = true
			}
			arr = append(arr, cv)
		}
		if hasZero {
			arr = append(arr, nil)
		}
		return bson.D{{Key: c.path, Value: bson.D{{Key: op, Value: arr}}}}, nil

	case condRange:
		info, err := typedField(idx, c.path, "range")
		if err != nil {
			return nil, err
		}
		if !info.ordered {
			return nil, errors.Wrapf(errors.InvalidArgument, "range on %q: not an ordered field", c.path)
		}
		cv, err := convertValue(c.values[0], info.elem, c.path)
		if err != nil {
			return nil, err
		}
		return bson.D{{Key: c.path, Value: bson.D{{Key: c.op, Value: cv}}}}, nil

	case condRaw:
		return c.raw, nil
	}
	return nil, errors.Wrap(errors.InvalidArgument, "unknown condition")
}

// equalityPaths are the paths an equality-type condition constrains.
func (c Cond) equalityPaths() []string {
	switch c.kind {
	case condMatch:
		out := make([]string, 0, len(c.pairs))
		for _, p := range c.pairs {
			out = append(out, p.path)
		}
		return out
	case condZero:
		return c.paths
	case condIn, condNotIn:
		return []string{c.path}
	}
	return nil
}

// fitsField checks that a set field's encoded value is one the entry field
// holds, so a separate filter struct cannot compare a string with a number or
// a document with a scalar. The numeric types match each other, as MongoDB
// compares them by value; a field typed as an interface holds anything.
func fitsField(p setField, info *fieldInfo) error {
	if typesFit(p.goType, info.elem, 0) {
		return nil
	}
	return errors.Wrapf(errors.InvalidArgument, "value of type %v does not fit field %q of type %v", p.goType, p.path, info.typ)
}

// typesFit reports whether a value of Go type got encodes to what a field of
// type want holds: the same type; any value for an interface field; slices
// and arrays whose elements fit; otherwise the same BSON type, the numeric
// types counting as one. Two different struct types never fit.
func typesFit(got, want reflect.Type, depth int) bool {
	got, want = deref(got), deref(want)
	switch {
	case got == want, want.Kind() == reflect.Interface:
		return true
	case depth > maxFieldDepth:
		return false
	}
	isList := func(t reflect.Type) bool { return t.Kind() == reflect.Slice || t.Kind() == reflect.Array }
	if isList(got) || isList(want) {
		// []byte encodes as binary, not as an array; it fits only itself
		if !isList(got) || !isList(want) || got.Elem().Kind() == reflect.Uint8 || want.Elem().Kind() == reflect.Uint8 {
			return false
		}
		return typesFit(got.Elem(), want.Elem(), depth+1)
	}
	if walkable(got) || walkable(want) {
		return false
	}
	g, w := bsonTypeOf(got), bsonTypeOf(want)
	return g != 0 && (g == w || (isNumericBSON(g) && isNumericBSON(w)))
}

// scalarField looks a path up and refuses it unless it holds a single value.
func scalarField(idx fieldIndex, path, name string) (*fieldInfo, error) {
	info, err := typedField(idx, path, name)
	if err != nil {
		return nil, err
	}
	if !info.scalar {
		return nil, errors.Wrapf(errors.InvalidArgument, "%s on %q: not a scalar field", name, path)
	}
	return info, nil
}

// convertValue converts v to the field's type, refusing what does not fit
// rather than letting it match nothing.
func convertValue(v any, target reflect.Type, path string) (any, error) {
	if v == nil {
		return nil, errors.Wrapf(errors.InvalidArgument, "nil value for %q", path)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, errors.Wrapf(errors.InvalidArgument, "nil value for %q", path)
		}
		rv = rv.Elem()
	}
	if rv.Type() == target {
		return rv.Interface(), nil
	}
	refuse := func() (any, error) {
		return nil, errors.Wrapf(errors.InvalidArgument, "value %v (%T) does not fit field %q of type %v", v, v, path, target)
	}
	out := reflect.New(target).Elem()
	switch {
	case isInt(target.Kind()):
		switch {
		case isInt(rv.Kind()):
			n := rv.Int()
			if out.OverflowInt(n) {
				return refuse()
			}
			out.SetInt(n)
		case isUint(rv.Kind()):
			u := rv.Uint()
			if u > math.MaxInt64 || out.OverflowInt(int64(u)) {
				return refuse()
			}
			out.SetInt(int64(u))
		case isFloat(rv.Kind()):
			f := rv.Float()
			// float64(math.MaxInt64) is 2^63, itself out of range
			if f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 || out.OverflowInt(int64(f)) {
				return refuse()
			}
			out.SetInt(int64(f))
		default:
			return refuse()
		}
	case isUint(target.Kind()):
		var u uint64
		switch {
		case isInt(rv.Kind()):
			n := rv.Int()
			if n < 0 {
				return refuse()
			}
			u = uint64(n)
		case isUint(rv.Kind()):
			u = rv.Uint()
		case isFloat(rv.Kind()):
			f := rv.Float()
			if f < 0 || f != math.Trunc(f) || f >= math.MaxInt64 {
				return refuse()
			}
			u = uint64(f)
		default:
			return refuse()
		}
		// the encoder writes unsigned values as signed 64-bit integers
		if u > math.MaxInt64 || out.OverflowUint(u) {
			return refuse()
		}
		out.SetUint(u)
	case isFloat(target.Kind()):
		switch {
		case isInt(rv.Kind()):
			out.SetFloat(float64(rv.Int()))
		case isUint(rv.Kind()):
			out.SetFloat(float64(rv.Uint()))
		case isFloat(rv.Kind()):
			if out.OverflowFloat(rv.Float()) {
				return refuse()
			}
			out.SetFloat(rv.Float())
		default:
			return refuse()
		}
	case target.Kind() == reflect.String && rv.Kind() == reflect.String:
		out.SetString(rv.String())
	case target.Kind() == reflect.Bool && rv.Kind() == reflect.Bool:
		out.SetBool(rv.Bool())
	default:
		return refuse()
	}
	return out.Interface(), nil
}

// isInt, isUint and isFloat group reflect kinds for value conversion.
func isInt(k reflect.Kind) bool {
	return k == reflect.Int || k == reflect.Int8 || k == reflect.Int16 || k == reflect.Int32 || k == reflect.Int64
}

func isUint(k reflect.Kind) bool {
	return k == reflect.Uint || k == reflect.Uint8 || k == reflect.Uint16 || k == reflect.Uint32 || k == reflect.Uint64
}

func isFloat(k reflect.Kind) bool { return k == reflect.Float32 || k == reflect.Float64 }

// encodeExtra encodes a filter argument once, exactly as given, and returns
// nil when it contributes nothing: nil, a typed nil, an empty map or slice
// document, or anything that encodes to a document with no elements. The
// encoded bytes are what the filter carries, so what was judged empty or not
// is what is sent.
func encodeExtra(f any) (bson.Raw, error) {
	if f == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(f)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}
	doc, err := encodeDocument(f)
	if err != nil {
		if (rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice) && rv.Len() == 0 {
			return nil, nil // an empty list is no document, and no condition
		}
		return nil, err
	}
	if emptyEncoded(doc) {
		return nil, nil
	}
	return doc, nil
}

// buildFilter turns conditions, plus any extra filter documents (a caller's
// filter argument, a cached table's scope), into one filter. It returns nil
// when there is nothing to filter on, so the caller's own nil filter goes
// through as it does today.
func buildFilter(entry reflect.Type, conds []Cond, extras ...any) (bson.D, error) {
	idx := indexFor(entry)
	elements := bson.A{}
	equality := map[string]bool{}
	ranged := map[string]bool{}

	for _, c := range conds {
		el, err := c.element(entry, idx)
		if err != nil {
			return nil, err
		}
		// every condition must constrain something, judged by what will be
		// sent: a condition that encodes to {} would match every row
		empty, err := encodesToNothing(el)
		if err != nil {
			return nil, errors.WrapErrf(errors.InvalidArgument, err, "a condition cannot be encoded")
		}
		if empty {
			return nil, errors.Wrap(errors.InvalidArgument, "a condition constrains nothing: it encodes to an empty document")
		}
		for _, p := range c.equalityPaths() {
			if equality[p] || ranged[p] {
				return nil, errors.Wrapf(errors.InvalidArgument, "field %q is constrained by more than one condition", p)
			}
			equality[p] = true
		}
		if c.kind == condRange {
			if equality[c.path] {
				return nil, errors.Wrapf(errors.InvalidArgument, "field %q is constrained by an equality condition and a range", c.path)
			}
			ranged[c.path] = true
		}
		elements = append(elements, el)
	}
	for _, f := range extras {
		doc, err := encodeExtra(f)
		if err != nil {
			return nil, errors.WrapErrf(errors.InvalidArgument, err, "a filter cannot be encoded")
		}
		if doc != nil {
			elements = append(elements, doc)
		}
	}

	switch len(elements) {
	case 0:
		return nil, nil
	case 1:
		if d, ok := elements[0].(bson.D); ok {
			return d, nil
		}
	}
	return bson.D{{Key: "$and", Value: elements}}, nil
}

// BuildFilter returns the filter the conditions translate to for entry type
// E, checked as an operation would check them. A table ANDs it with the
// filter argument it is given and, on a CachedTable, with the table's
// configured filter. With no conditions it returns an empty document.
//
// It is meant for code that applies FindOptions itself, such as wrappers and
// test fakes (see FindOptions.Conds): they can compare or assert this
// document, though they cannot evaluate it in memory.
func BuildFilter[E any](conds ...Cond) (bson.D, error) {
	f, err := buildFilter(reflect.TypeOf((*E)(nil)).Elem(), conds)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return bson.D{}, nil
	}
	return f, nil
}
