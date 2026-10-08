// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package yamlkeys

import (
	stderrors "errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/go-core-stack/core/errors"
)

type testInner struct {
	Port int `yaml:"port"`
}

type testBase struct {
	Host string `yaml:"host"`
}

type testDoc struct {
	testBase `yaml:",inline"`
	Name     string               `yaml:"name"`
	Inner    testInner            `yaml:"inner"`
	List     []testInner          `yaml:"list"`
	Labels   map[string]testInner `yaml:"labels"`
	Skipped  string               `yaml:"-"`
}

type testCatchAll struct {
	Name string         `yaml:"name"`
	Rest map[string]any `yaml:",inline"`
}

// selfDecoding reads whatever mapping it is given.
type selfDecoding struct{ keys int }

func (s *selfDecoding) UnmarshalYAML(n *yaml.Node) error {
	s.keys = len(n.Content) / 2
	return nil
}

type testWithSelf struct {
	Plugin selfDecoding `yaml:"plugin"`
}

func TestDecode_ReportsUnknownKeysAndDecodesTheRest(t *testing.T) {
	raw := `
name: x
namee: y
host: h
inner: {port: 5, portt: 6}
list: [{port: 1, nope: 1}]
labels: {k: {port: 2, zz: 1}}
"-": 1
`
	var d testDoc
	unknown, err := Decode([]byte(raw), &d)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := []string{
		"line 3: field namee not found in type yamlkeys.testDoc",
		"line 5: field portt not found in type yamlkeys.testInner",
		"line 6: field nope not found in type yamlkeys.testInner",
		"line 7: field zz not found in type yamlkeys.testInner",
		"line 8: field - not found in type yamlkeys.testDoc",
	}
	if !reflect.DeepEqual(unknown, want) {
		t.Errorf("unknown:\n got %q\nwant %q", unknown, want)
	}
	if d.Name != "x" || d.Host != "h" || d.Inner.Port != 5 || len(d.List) != 1 || d.List[0].Port != 1 || d.Labels["k"].Port != 2 {
		t.Errorf("known keys not decoded: %+v", d)
	}
}

func TestDecode_NothingUnknown(t *testing.T) {
	var d testDoc
	unknown, err := Decode([]byte("name: x\ninner: {port: 1}\n"), &d)
	if err != nil || unknown != nil {
		t.Fatalf("got unknown=%q err=%v", unknown, err)
	}
}

// An inline map takes every key the struct does not name, so nothing is
// unknown; a type that decodes itself reads what it is given.
func TestDecode_KeysTakenByTheType(t *testing.T) {
	var c testCatchAll
	unknown, err := Decode([]byte("name: x\nother: 1\n"), &c)
	if err != nil || unknown != nil || c.Rest["other"] != 1 {
		t.Fatalf("inline map: unknown=%q err=%v rest=%v", unknown, err, c.Rest)
	}
	var s testWithSelf
	unknown, err = Decode([]byte("plugin: {a: 1, b: 2}\n"), &s)
	if err != nil || unknown != nil || s.Plugin.keys != 2 {
		t.Fatalf("self-decoding: unknown=%q err=%v keys=%d", unknown, err, s.Plugin.keys)
	}
}

// Merge keys and aliases are yaml.v3's to resolve: a key from a merged or
// aliased mapping is reported at the line it is written on.
func TestDecode_MergesAndAliases(t *testing.T) {
	raw := `
base: &b {port: 1, bogus: 2}
inner: {<<: *b}
list: [*b, *b]
`
	var d struct {
		Base  map[string]int `yaml:"base"`
		Inner testInner      `yaml:"inner"`
		List  []testInner    `yaml:"list"`
	}
	unknown, err := Decode([]byte(raw), &d)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := []string{
		"line 2: field bogus not found in type yamlkeys.testInner",
		"line 2: field bogus not found in type yamlkeys.testInner",
		"line 2: field bogus not found in type yamlkeys.testInner",
	}
	if !reflect.DeepEqual(unknown, want) {
		t.Errorf("unknown:\n got %q\nwant %q", unknown, want)
	}
	if d.Inner.Port != 1 || len(d.List) != 2 {
		t.Errorf("merged and aliased values not decoded: %+v", d)
	}
}

// A type error still fails the decode; the unknown keys found are returned
// with it, and the error carries only the type errors.
func TestDecode_TypeErrorStillFails(t *testing.T) {
	var d testDoc
	unknown, err := Decode([]byte("name: x\ninner: {port: notanint}\nextra: 1\n"), &d)
	if !errors.IsInvalidArgument(err) {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	var te *yaml.TypeError
	if !stderrors.As(err, &te) || len(te.Errors) != 1 || !strings.Contains(te.Errors[0], "cannot unmarshal") {
		t.Errorf("expected the one type error in a *yaml.TypeError, got %v", err)
	}
	if want := []string{"line 3: field extra not found in type yamlkeys.testDoc"}; !reflect.DeepEqual(unknown, want) {
		t.Errorf("unknown: got %q, want %q", unknown, want)
	}
}

func TestDecode_ParseError(t *testing.T) {
	var d testDoc
	unknown, err := Decode([]byte("name: [unclosed\n"), &d)
	if !errors.IsInvalidArgument(err) || unknown != nil {
		t.Fatalf("expected InvalidArgument and nothing reported, got unknown=%q err=%v", unknown, err)
	}
}

// An empty document is not an error, as with yaml.Unmarshal; only the first
// document of a stream is decoded.
func TestDecode_EmptyAndMultiDocument(t *testing.T) {
	for _, raw := range []string{"", "# only a comment\n"} {
		d := testDoc{Name: "kept"}
		unknown, err := Decode([]byte(raw), &d)
		if err != nil || unknown != nil || d.Name != "kept" {
			t.Errorf("%q: unknown=%q err=%v name=%q", raw, unknown, err, d.Name)
		}
	}
	var d testDoc
	unknown, err := Decode([]byte("name: first\n---\nname: second\nextra: 1\n"), &d)
	if err != nil || unknown != nil || d.Name != "first" {
		t.Errorf("multi-document: unknown=%q err=%v name=%q", unknown, err, d.Name)
	}
}

// TestDecode_MessageFormat pins the yaml.v3 message Decode recognises, for
// keys of any spelling. If an upgrade changes it, unknown keys turn into
// errors here before they do for a caller.
func TestDecode_MessageFormat(t *testing.T) {
	for _, key := range []string{"plain", "with space", "with.dot", "line\nbreak", "€uro", "found in type x"} {
		raw, err := yaml.Marshal(map[string]int{key: 1})
		if err != nil {
			t.Fatalf("marshal %q: %v", key, err)
		}
		var d testDoc
		unknown, err := Decode(raw, &d)
		if err != nil || len(unknown) != 1 {
			t.Errorf("key %q: unknown=%q err=%v", key, unknown, err)
		}
	}
}

// wrapsItsError decodes itself through the older UnmarshalYAML form and
// wraps the error it gets, as callers commonly do.
type wrapsItsError struct {
	Port int `yaml:"port"`
}

func (w *wrapsItsError) UnmarshalYAML(unmarshal func(any) error) error {
	type plain wrapsItsError
	if err := unmarshal((*plain)(w)); err != nil {
		return fmt.Errorf("decoding w: %w", err)
	}
	return nil
}

// A *yaml.TypeError wrapped by a type's own UnmarshalYAML aborted the decode
// and is not a list of unknown keys: Decode must fail, not report success
// with a partly decoded value.
func TestDecode_WrappedTypeErrorFails(t *testing.T) {
	var d struct {
		W    wrapsItsError `yaml:"w"`
		Name string        `yaml:"name"`
	}
	unknown, err := Decode([]byte("w: {port: 1, bogus: 2}\nname: x\n"), &d)
	if !errors.IsInvalidArgument(err) || unknown != nil {
		t.Fatalf("expected InvalidArgument and nothing reported, got unknown=%q err=%v", unknown, err)
	}
}

// Only yaml.v3's unknown-field message is a report. A duplicate key whose
// text imitates that message is still an error.
func TestDecode_ImitatedMessageIsAnError(t *testing.T) {
	raw := "'line 1: field a not found in type b': 1\n'line 1: field a not found in type b': 2\n"
	var d map[string]int
	unknown, err := Decode([]byte(raw), &d)
	if !errors.IsInvalidArgument(err) || unknown != nil {
		t.Fatalf("expected InvalidArgument and nothing reported, got unknown=%q err=%v", unknown, err)
	}
}

// passesItsError decodes itself and returns node.Decode's error unchanged.
type passesItsError struct {
	Port int `yaml:"port"`
}

func (p *passesItsError) UnmarshalYAML(n *yaml.Node) error {
	type plain passesItsError
	return n.Decode((*plain)(p))
}

// A *yaml.TypeError a type returns unchanged is kept by yaml.v3, which
// decodes on: the rest is decoded, and the type error and unknown keys are
// split as usual.
func TestDecode_UnwrappedTypeErrorFromATypeIsSplit(t *testing.T) {
	var d struct {
		P    passesItsError `yaml:"p"`
		Name string         `yaml:"name"`
	}
	unknown, err := Decode([]byte("p: {port: notanint}\nname: x\nextra: 1\n"), &d)
	var te *yaml.TypeError
	if !errors.IsInvalidArgument(err) || !stderrors.As(err, &te) || len(te.Errors) != 1 || !strings.Contains(te.Errors[0], "cannot unmarshal") {
		t.Fatalf("expected the one type error, got %v", err)
	}
	if len(unknown) != 1 || !strings.Contains(unknown[0], "field extra not found") || d.Name != "x" {
		t.Errorf("expected the decode to run on: unknown=%q name=%q", unknown, d.Name)
	}
}
