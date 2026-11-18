// Copyright © 2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package db

import (
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// eventMatcher decides, in the process, whether a change event matches the
// $match of one Watch. The stream shared by the watches on a database
// sends the process every change in it, so each watch's filter is applied
// here before its callback is called.
//
// It holds the part of the query language the filters given to Watch use:
// a field path compared for equality, implicitly or with $eq, or for
// membership with $in, against strings and booleans, every condition
// having to hold. It follows the server on what such a filter matches: an
// array matches when one of its elements does, and a path crosses an array
// of documents into each of them. A filter using anything else, numbers
// included, is not shared; its watch keeps a stream of its own (see
// parseWatchFilter). Numbers are left out because the server compares them
// by value across int32, int64, double and Decimal128, which a match here
// would have to follow exactly or miss changes without a sign.
type eventMatcher []fieldCondition

// fieldCondition holds when the value at path equals one of values.
type fieldCondition struct {
	path   []string
	values []any
}

// parseWatchFilter reads the pipeline given to Watch and returns the
// matcher for it, nil when the pipeline matches every change. The matcher
// is built from a copy of the $match document, so a caller changing its
// filter after Watch returns does not change it. ok is false when the
// pipeline is anything other than nothing or one $match stage this matcher
// can apply.
func parseWatchFilter(pipeline mongo.Pipeline) (m eventMatcher, ok bool) {
	if len(pipeline) == 0 {
		return nil, true
	}
	if len(pipeline) != 1 || len(pipeline[0]) != 1 || pipeline[0][0].Key != "$match" {
		return nil, false
	}
	if _, ok := documentFields(pipeline[0][0].Value); !ok {
		return nil, false
	}
	raw, err := bson.Marshal(pipeline[0][0].Value)
	if err != nil {
		return nil, false
	}
	var fields bson.D
	if err := bson.Unmarshal(raw, &fields); err != nil {
		return nil, false
	}
	if len(fields) == 0 {
		return nil, true
	}
	for _, f := range fields {
		if f.Key == "" || strings.HasPrefix(f.Key, "$") {
			return nil, false
		}
		path := strings.Split(f.Key, ".")
		for _, p := range path {
			// a numeric component can name an array index as well as a
			// field, which this matcher does not follow
			if p == "" || isIndex(p) {
				return nil, false
			}
		}
		values, ok := conditionValues(f.Value)
		if !ok {
			return nil, false
		}
		m = append(m, fieldCondition{path: path, values: values})
	}
	return m, true
}

// documentFields returns the fields of a filter document given as bson.D or
// bson.M, the two forms the platform's filters use.
func documentFields(v any) (bson.D, bool) {
	switch d := v.(type) {
	case bson.D:
		return d, true
	case bson.M:
		fields := bson.D{}
		for k, val := range d {
			fields = append(fields, bson.E{Key: k, Value: val})
		}
		return fields, true
	default:
		return nil, false
	}
}

// conditionValues returns the values a field condition accepts: the value
// itself, the value of $eq, or the values of $in.
func conditionValues(v any) ([]any, bool) {
	if isScalar(v) {
		return []any{v}, true
	}
	ops, ok := documentFields(v)
	if !ok || len(ops) != 1 {
		return nil, false
	}
	switch ops[0].Key {
	case "$eq":
		if isScalar(ops[0].Value) {
			return []any{ops[0].Value}, true
		}
	case "$in":
		// the copy of the filter holds every list as a bson.A
		values, ok := ops[0].Value.(bson.A)
		if !ok {
			return nil, false
		}
		for _, val := range values {
			if !isScalar(val) {
				return nil, false
			}
		}
		return values, true
	}
	return nil, false
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, bool:
		return true
	}
	return false
}

func isIndex(p string) bool {
	for _, r := range p {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// matches reports whether the change event satisfies every condition.
func (m eventMatcher) matches(event bson.Raw) bool {
	root := bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: event}
	for _, c := range m {
		if !matchPath(root, c.path, c.values) {
			return false
		}
	}
	return true
}

// matchPath reports whether a value at path below v equals one of values,
// crossing arrays of documents as the server does.
func matchPath(v bson.RawValue, path []string, values []any) bool {
	if len(path) == 0 {
		return matchValue(v, values)
	}
	switch v.Type {
	case bson.TypeEmbeddedDocument:
		next, err := v.Document().LookupErr(path[0])
		if err != nil {
			return false
		}
		return matchPath(next, path[1:], values)
	case bson.TypeArray:
		elems, err := v.Array().Values()
		if err != nil {
			return false
		}
		for _, e := range elems {
			if e.Type == bson.TypeEmbeddedDocument && matchPath(e, path, values) {
				return true
			}
		}
	}
	return false
}

// matchValue reports whether v, or one of its elements when it is an
// array, equals one of values.
func matchValue(v bson.RawValue, values []any) bool {
	if v.Type == bson.TypeArray {
		elems, err := v.Array().Values()
		if err != nil {
			return false
		}
		for _, e := range elems {
			if e.Type != bson.TypeArray && matchValue(e, values) {
				return true
			}
		}
		return false
	}
	for _, want := range values {
		if equalScalar(v, want) {
			return true
		}
	}
	return false
}

// equalScalar compares a value from an event with one from a filter, a
// string or a boolean, which equal only a value of the same type.
func equalScalar(v bson.RawValue, want any) bool {
	switch w := want.(type) {
	case string:
		s, ok := v.StringValueOK()
		return ok && s == w
	case bool:
		b, ok := v.BooleanOK()
		return ok && b == w
	}
	return false
}
