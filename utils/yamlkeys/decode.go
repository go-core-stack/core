// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Package yamlkeys decodes a YAML document tolerantly while still reporting
// the keys the target type does not read.
//
// Decoding YAML into a struct is either strict, failing on every key the
// struct does not declare, including keys a newer or older version of the
// document carries, or tolerant, letting a misspelt key silently become
// "not set". Decode decodes tolerantly and also returns the keys a strict
// decode would have refused, so a caller can report them (a log line, a
// metric) without failing.
//
// It relies on gopkg.in/yaml.v3 itself: with KnownFields set, yaml.v3
// decodes the whole document and returns every unknown key in one
// *yaml.TypeError, alongside any other type errors. Decode separates the
// two. Which keys count as unknown is therefore yaml.v3's own answer,
// including for inline structs and maps, merge keys and aliases.
//
// Types that decode themselves see the strict setting differently:
//   - A type with the older UnmarshalYAML(func(any) error) error receives
//     the unknown keys under it as a *yaml.TypeError from that function.
//     Returned unchanged, yaml.v3 keeps them and carries on, so they are
//     reported, but the type's own code after that call does not run.
//     Wrapped, the decode stops and fails.
//   - A type with UnmarshalYAML(*yaml.Node) error that decodes through
//     node.Decode does so without KnownFields, so unknown keys under it are
//     not reported.
package yamlkeys

import (
	"bytes"
	stderrors "errors"
	"io"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/go-core-stack/core/errors"
)

// unknownField matches the message yaml.v3 gives for a key the target type
// does not declare (decode.go, mappingStruct). A quoted key can hold a
// newline, hence (?s). If a later yaml.v3 words it differently, those keys
// fall to the other errors and Decode fails, as a strict decode would, and
// TestDecode_MessageFormat fails with it.
var unknownField = regexp.MustCompile(`(?s)^line \d+: field .+ not found in type .+$`)

// Decode decodes the first YAML document in raw into v, as yaml.v3 does,
// and returns the keys v's type does not read instead of failing on them.
//
// Each unknown key is reported as yaml.v3 states it, for example
// "line 3: field portt not found in type config.Server", in the order
// yaml.v3 decodes them. A key reached through an alias is reported at the
// line it is written on, once each time yaml.v3 decodes it through an
// alias, and each entry holds the whole key. The entries hold key names,
// never values; a quoted or !!binary key can put any bytes in one, control
// characters included, so log them as structured values.
//
// The result is yaml.v3's own strict error list, so it costs what a strict
// decode costs: a document that uses an anchored mapping with an unknown
// key through many aliases yields an entry per use. Bound the size of
// documents from untrusted sources before decoding them.
//
// An empty document leaves v unchanged and reports nothing. Any other
// error is returned as InvalidArgument and carries yaml.v3's error. With
// type errors, such as a string where a number is wanted, the decode ran to
// the end, the error carries only those type errors, and the unknown keys
// are still returned. A *yaml.TypeError that a type's own UnmarshalYAML
// returns unchanged is kept by yaml.v3 like its own, so it is split the
// same way. A parse error, or any other error from a type's own
// UnmarshalYAML, a wrapped *yaml.TypeError included, stops the decode, and
// nothing is reported.
func Decode(raw []byte, v any) ([]string, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	err := dec.Decode(v)
	if err == nil || stderrors.Is(err, io.EOF) {
		return nil, nil
	}
	// Only the *yaml.TypeError yaml.v3 itself returns, unwrapped, means the
	// decode ran to the end. One wrapped by a type's own UnmarshalYAML
	// aborted it, and must not be read as a list of unknown keys.
	te, ok := err.(*yaml.TypeError)
	if !ok {
		return nil, errors.WrapErr(errors.InvalidArgument, err)
	}
	var unknown, other []string
	for _, msg := range te.Errors {
		if unknownField.MatchString(msg) {
			unknown = append(unknown, msg)
		} else {
			other = append(other, msg)
		}
	}
	if len(other) > 0 {
		return unknown, errors.WrapErr(errors.InvalidArgument, &yaml.TypeError{Errors: other})
	}
	return unknown, nil
}
