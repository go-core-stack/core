// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/utils"
)

// These tests need no database: they check the field index and how
// conditions translate and are refused.

type condStatus string

type condSpec struct {
	Owner string `bson:"owner,omitempty"`
	Level int    `bson:"level,omitempty"`
}

type CondEmbedded struct {
	Region string `bson:"region,omitempty"`
}

type condEntry struct {
	Status       *condStatus       `bson:"status,omitempty"`
	Worker       *string           `bson:"worker,omitempty"`
	Attempts     int64             `bson:"attempts,omitempty"`
	Used         *int32            `bson:"used,omitempty"`
	Ratio        float64           `bson:"ratio,omitempty"`
	Deleted      bool              `bson:"deleted,omitempty"`
	At           time.Time         `bson:"at,omitempty"`
	Tags         []string          `bson:"tags,omitempty"`
	Labels       map[string]string `bson:"labels,omitempty"`
	Spec         *condSpec         `bson:"spec,omitempty"`
	Untagged     string
	Skipped      string   `bson:"-"`
	Any          any      `bson:"any,omitempty"`
	Tag          ownerTag `bson:"tag,omitempty"`
	CondEmbedded `bson:",inline"`
}

var condEntryType = reflect.TypeOf(condEntry{})

// sameDoc compares two filter documents by their encoding: values may be
// Go values or already-encoded bson.RawValues.
func sameDoc(a, b bson.D) bool {
	x, err1 := bson.Marshal(a)
	y, err2 := bson.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

func wantInvalid(t *testing.T, err error) {
	t.Helper()
	if err == nil || !errors.IsInvalidArgument(err) {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
}

func TestFieldIndexFollowsTheEncoder(t *testing.T) {
	idx := indexFor(condEntryType)
	for _, path := range []string{"status", "attempts", "spec.owner", "spec.level", "untagged", "region", "tags", "labels", "at"} {
		if _, ok := idx[path]; !ok {
			t.Errorf("path %q missing from the index", path)
		}
	}
	for _, path := range []string{"Skipped", "skipped", "condembedded"} {
		if _, ok := idx[path]; ok {
			t.Errorf("path %q should not be indexed", path)
		}
	}
	if !idx["attempts"].numeric || idx["status"].numeric || !idx["used"].numeric {
		t.Error("numeric classification is wrong")
	}
	if !idx["at"].ordered || idx["deleted"].ordered || !idx["deleted"].scalar {
		t.Error("ordered / scalar classification is wrong")
	}
	if idx["tags"].scalar || !idx["labels"].hasMap || idx["spec"].leaf {
		t.Error("non-scalar classification is wrong")
	}
}

func TestMatchTranslatesSetFieldsOnly(t *testing.T) {
	st := condStatus("running")
	f, err := buildFilter(condEntryType, []Cond{Match(&condEntry{Status: &st, Spec: &condSpec{Owner: "a"}, CondEmbedded: CondEmbedded{Region: "eu"}})})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "status", Value: condStatus("running")}, {Key: "spec.owner", Value: "a"}, {Key: "region", Value: "eu"}}
	if !sameDoc(f, want) {
		t.Errorf("filter = %v, want %v", f, want)
	}
}

func TestConditionsCopyWhatTheyAreGiven(t *testing.T) {
	worker := "a"
	e := &condEntry{Worker: &worker, Tags: []string{"x"}}
	c := Match(e)
	values := []any{"queued"}
	in := In("status", values...)

	worker, e.Tags[0], values[0] = "b", "y", "done"
	e.Attempts = 9

	f, err := buildFilter(condEntryType, []Cond{c, in})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "worker", Value: "a"}, {Key: "tags", Value: []string{"x"}}},
		bson.D{{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{condStatus("queued")}}}}},
	}}}
	if !sameDoc(f, want) {
		t.Errorf("filter = %v, want %v", f, want)
	}
}

func TestValuesAreConvertedToTheFieldType(t *testing.T) {
	f, err := buildFilter(condEntryType, []Cond{In("status", "queued", condStatus("requeued")), LessEq("attempts", 3), GreaterEq("used", int64(2))})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{condStatus("queued"), condStatus("requeued")}}}}},
		bson.D{{Key: "attempts", Value: bson.D{{Key: "$lte", Value: int64(3)}}}},
		bson.D{{Key: "used", Value: bson.D{{Key: "$gte", Value: int32(2)}}}},
	}}}
	if !sameDoc(f, want) {
		t.Errorf("filter = %v, want %v", f, want)
	}
}

func TestZeroValuesAlsoMatchNullAndAbsent(t *testing.T) {
	f, err := buildFilter(condEntryType, []Cond{MatchZero("deleted"), In("attempts", 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "deleted", Value: bson.D{{Key: "$in", Value: bson.A{false, nil}}}}},
		bson.D{{Key: "attempts", Value: bson.D{{Key: "$in", Value: bson.A{int64(0), int64(1), nil}}}}},
	}}}
	if !sameDoc(f, want) {
		t.Errorf("filter = %v, want %v", f, want)
	}
}

func TestExtraFiltersAreAndedAndNilsLeftOut(t *testing.T) {
	var typedNil bson.M
	f, err := buildFilter(condEntryType, []Cond{In("status", "queued")}, nil, typedNil, bson.D{}, bson.M{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	and, ok := f[0].Value.(bson.A)
	if f[0].Key != "$and" || !ok || len(and) != 2 {
		t.Fatalf("filter = %v, want the condition and the one non-empty filter under $and", f)
	}
	if f, _ := buildFilter(condEntryType, nil, nil); f != nil {
		t.Errorf("no conditions and a nil filter = %v, want nil", f)
	}
}

func TestBuildFilter(t *testing.T) {
	f, err := BuildFilter[condEntry]()
	if err != nil || f == nil || len(f) != 0 {
		t.Errorf("BuildFilter() = %v, %v; want an empty document", f, err)
	}
	f, err = BuildFilter[condEntry](Less("attempts", 5))
	want := bson.D{{Key: "attempts", Value: bson.D{{Key: "$lt", Value: int64(5)}}}}
	if err != nil || !sameDoc(f, want) {
		t.Errorf("BuildFilter = %v, %v; want %v", f, err, want)
	}
	_, err = BuildFilter[condEntry](In("nope", 1))
	wantInvalid(t, err)
}

func TestConditionRefusals(t *testing.T) {
	for name, c := range map[string][]Cond{
		"unknown path":            {In("stauts", "queued")},
		"Match with no set field": {Match(&condEntry{})},
		"Match of nil":            {Match[condEntry](nil)},
		"Match of a struct naming an unknown path": {Match(&struct {
			State string `bson:"state"`
		}{State: "x"})},
		"Match of a struct with a value that does not fit": {Match(&struct {
			Attempts string `bson:"attempts"`
		}{Attempts: "1"})},
		"Match on a slice of maps": {Match(&struct {
			Tags []map[string]string `bson:"tags"`
		}{Tags: []map[string]string{{"a": "b"}}})},
		"Match on a map":                 {Match(&condEntry{Labels: map[string]string{"a": "b"}})},
		"In with no values":              {In("status")},
		"NotIn with no values":           {NotIn("status")},
		"In on a slice":                  {In("tags", "x")},
		"In on a nested struct":          {In("spec", "x")},
		"MatchZero on a slice":           {MatchZero("tags")},
		"MatchZero with no path":         {MatchZero()},
		"range on a bool":                {Less("deleted", true)},
		"range on a slice":               {Less("tags", "x")},
		"nil value":                      {In("status", nil)},
		"typed nil value":                {In("worker", (*string)(nil))},
		"int for a string":               {In("status", 1)},
		"string for an int":              {In("attempts", "1")},
		"overflow":                       {In("used", int64(1)<<40)},
		"fraction for an int":            {In("attempts", 1.5)},
		"empty Raw":                      {Raw(bson.D{})},
		"a zero-value Cond":              {Cond{}},
		"a zero-value Cond among others": {In("status", "a"), Cond{}},
		"Raw of a struct that encodes to nothing": {Raw(&struct {
			S string `bson:",omitempty"`
		}{})},
		"Raw of an empty struct":      {Raw(struct{}{})},
		"Raw of an empty bson.Raw":    {Raw(bson.Raw{5, 0, 0, 0, 0})},
		"nil Raw":                     {Raw(nil)},
		"same path twice":             {In("status", "a"), NotIn("status", "b")},
		"equality and range together": {MatchZero("attempts"), Less("attempts", 3)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildFilter(condEntryType, c)
			wantInvalid(t, err)
		})
	}

	// two ranges on one path are a window, not a conflict
	if _, err := buildFilter(condEntryType, []Cond{Greater("attempts", 1), Less("attempts", 5)}); err != nil {
		t.Errorf("a window was refused: %v", err)
	}
	// a pointer to a value of the field's type is accepted
	if _, err := buildFilter(condEntryType, []Cond{In("worker", utils.Pointer("a"))}); err != nil {
		t.Errorf("a pointer value was refused: %v", err)
	}
}

func TestFindOptionsCarryWhere(t *testing.T) {
	o := &FindOptions{}
	for _, opt := range []FindOption{WithLimit(5), Where(In("status", "queued")), Where(Less("attempts", 3))} {
		opt(o)
	}
	if *o.Limit != 5 || len(o.Conds()) != 2 {
		t.Errorf("options = %+v, want the limit and two conditions", o)
	}
	f, err := findFilter[condEntry](o, bson.M{"x": 1})
	if err != nil || f == nil {
		t.Fatalf("findFilter = %v, %v", f, err)
	}

	empty := &FindOptions{}
	Where()(empty)
	_, err = findFilter[condEntry](empty, nil)
	wantInvalid(t, err)

	plain := &FindOptions{}
	f, err = findFilter[condEntry](plain, bson.M{"x": 1})
	if err != nil || !reflect.DeepEqual(f, bson.M{"x": 1}) {
		t.Errorf("without Where the filter must pass through unchanged, got %v, %v", f, err)
	}
}

// An entry type the index cannot fully describe must not stop a table from
// starting; only conditions naming such a field are refused.
func TestUnusualEntryTypesStillIndex(t *testing.T) {
	type node struct {
		Next *node         `bson:"next,omitempty"`
		Ch   chan int      `bson:"-"`
		Fn   func()        `bson:"-"`
		Raw  bson.Raw      `bson:"raw,omitempty"`
		M    map[int][]any `bson:"m,omitempty"`
		Name string        `bson:"name"`
	}
	idx := indexFor(reflect.TypeOf(node{}))
	if _, ok := idx["name"]; !ok {
		t.Error("a plain field next to unusual ones was not indexed")
	}
	if _, ok := idx["next.next.name"]; !ok {
		t.Error("a recursive type was not walked to depth")
	}
}

func TestConditionsCopyPointerValues(t *testing.T) {
	v, n := "queued", int64(3)
	in, less := In("status", &v), Less("attempts", &n)
	v, n = "changed", 99
	f, err := buildFilter(condEntryType, []Cond{in, less})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{condStatus("queued")}}}}},
		bson.D{{Key: "attempts", Value: bson.D{{Key: "$lt", Value: int64(3)}}}},
	}}}
	if !sameDoc(f, want) {
		t.Errorf("filter = %v, want the values as they were when the conditions were built", f)
	}
}

func TestNumericConversionBounds(t *testing.T) {
	type unsigned struct {
		U uint32 `bson:"u"`
		N int64  `bson:"n"`
	}
	ut := reflect.TypeOf(unsigned{})
	for name, c := range map[string]Cond{
		"2^63 as a float into int64":  GreaterEq("n", float64(math.MaxInt64)),
		"a uint64 above MaxInt64":     In("n", uint64(math.MaxInt64)+1),
		"a negative into an unsigned": In("u", -1),
		"overflow of an unsigned":     In("u", int64(math.MaxUint32)+1),
		"a fraction into an unsigned": In("u", 1.5),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildFilter(ut, []Cond{c})
			wantInvalid(t, err)
		})
	}
	if _, err := buildFilter(ut, []Cond{In("u", 7), LessEq("n", 1e3)}); err != nil {
		t.Errorf("values that fit were refused: %v", err)
	}
}

// Types the driver encodes through its own codecs are values, not documents
// to walk: compared whole, and scalar where their encoding is.
func TestRegistryEncodedTypesAreValues(t *testing.T) {
	type typed struct {
		ID    bson.ObjectID   `bson:"oid,omitempty"`
		Ts    bson.Timestamp  `bson:"ts,omitempty"`
		Price bson.Decimal128 `bson:"price,omitempty"`
		Bin   bson.Binary     `bson:"bin,omitempty"`
		At    time.Time       `bson:"at,omitempty"`
		Name  string          `bson:"name,omitempty"`
	}
	tt := reflect.TypeOf(typed{})
	idx := indexFor(tt)
	for path, ordered := range map[string]bool{"oid": true, "ts": true, "price": true, "at": true} {
		info := idx[path]
		if info == nil || !info.leaf || !info.scalar || info.ordered != ordered {
			t.Errorf("%s classified as %+v", path, info)
		}
	}
	if idx["bin"] == nil || idx["bin"].scalar || idx["ts.t"] != nil {
		t.Error("bson.Binary must be a non-scalar leaf, and Timestamp must not be walked")
	}
	d, _ := bson.ParseDecimal128("1.5")
	oid := bson.NewObjectID()
	f, err := buildFilter(tt, []Cond{Match(&typed{Ts: bson.Timestamp{T: 5, I: 1}, Price: d, Name: "n"}), In("oid", oid)})
	if err != nil {
		t.Fatal(err)
	}
	first := f[0].Value.(bson.A)[0].(bson.D)
	if len(first) != 3 || first[0].Key != "ts" || first[1].Key != "price" {
		t.Errorf("Match on registry types = %v; want ts and price compared whole, and name", first)
	}
}

func TestTagsAndDominanceFollowTheEncoder(t *testing.T) {
	type Inner struct {
		Status int    `bson:"status"`
		Zone   string `bson:"zone"`
	}
	type outer struct {
		Status string `bson:"status"`
		Inner  `bson:"inline"`
	}
	ot := reflect.TypeOf(outer{})
	idx := indexFor(ot)
	if idx["status"] == nil || idx["status"].elem.Kind() != reflect.String || idx["zone"] == nil || idx["inline.zone"] != nil {
		t.Fatalf("index = %v; want the outer status and the inlined zone", idx)
	}
	f, err := buildFilter(ot, []Cond{Match(&outer{Status: "a", Inner: Inner{Status: 2, Zone: "z"}})})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "status", Value: "a"}, {Key: "zone", Value: "z"}}
	if !sameDoc(f, want) {
		t.Errorf("Match = %v, want the shadowed inner status left out: %v", f, want)
	}

	// only the hidden field set: refused, not a filter that matches nothing
	_, err = buildFilter(ot, []Cond{Match(&outer{Inner: Inner{Status: 3}})})
	wantInvalid(t, err)
}

type rev int

func (r rev) MarshalBSONValue() (byte, []byte, error) {
	t, b, err := bson.MarshalValue(fmt.Sprintf("v%d", int(r)))
	return byte(t), b, err
}

// ptrStatus encodes itself through its pointer type only, as an int32: the
// driver runs that encoder for a field of a row it was given a pointer to.
type ptrStatus string

func (s *ptrStatus) MarshalBSONValue() (byte, []byte, error) {
	t, b, err := bson.MarshalValue(int32(len(*s)))
	return byte(t), b, err
}

// scaled encodes itself through its pointer type only, as a hundredfold
// int64: its Go value is not what it stores.
type scaled int64

func (s *scaled) MarshalBSONValue() (byte, []byte, error) {
	t, b, err := bson.MarshalValue(int64(*s) * 100)
	return byte(t), b, err
}

// A field whose type encodes itself stores what its encoder writes, not its
// Go shape, so typed conditions and increments refuse it and point to Raw;
// fields beside it are unaffected.
func TestSelfEncodingFieldsAreReachedThroughRaw(t *testing.T) {
	type entry struct {
		Rev    rev                   `bson:"rev"`
		Status ptrStatus             `bson:"status"`
		Revs   []rev                 `bson:"revs"`
		Plain  string                `bson:"plain"`
		Inner  struct{ S ptrStatus } `bson:"inner"`
	}
	et := reflect.TypeOf(entry{})
	refused := map[string]Cond{
		"Match value":        Match(&entry{Rev: 5}),
		"Match pointer-only": Match(&entry{Status: "abc"}),
		"Match list":         Match(&entry{Revs: []rev{1}}),
		"Match nested": Match(&struct {
			S ptrStatus `bson:"inner.s"`
		}{S: "x"}),
		"filter struct value": Match(&struct {
			P rev `bson:"plain"`
		}{P: 1}),
		// plain values against a field holding a self-encoding type in a
		// list or a nested struct: refused for the field, not the value
		"plain list against self-encoding list": Match(&struct {
			Revs []int `bson:"revs"`
		}{Revs: []int{1}}),
		"plain value against nested self-encoding": Match(&struct {
			S string `bson:"inner.s"`
		}{S: "x"}),
		"MatchZero":   MatchZero("status"),
		"In":          In("rev", rev(5)),
		"In on plain": In("plain", rev(5)),
		"NotIn":       NotIn("status", "abc"),
		"Less":        Less("rev", 5),
	}
	for name, c := range refused {
		_, err := buildFilter(et, []Cond{c})
		wantInvalid(t, err)
		if !strings.Contains(err.Error(), "Raw") {
			t.Errorf("%s: %v; want it to point to Raw", name, err)
		}
	}
	if _, err := buildFilter(et, []Cond{Match(&entry{Plain: "p"})}); err != nil {
		t.Errorf("a plain field beside self-encoding ones was refused: %v", err)
	}

	// Raw encodes its argument as the driver would: given a pointer, as a
	// row is written, the pointer-type encoder runs
	f, err := buildFilter(et, []Cond{Raw(&struct {
		Status ptrStatus `bson:"status"`
	}{Status: "abc"})})
	if err != nil {
		t.Fatal(err)
	}
	if !sameDoc(f, bson.D{{Key: "$and", Value: bson.A{bson.D{{Key: "status", Value: int32(3)}}}}}) {
		t.Errorf("Raw = %v; want status encoded by its own encoder, as int32 3", f)
	}
}

// fixedDoc's Go fields are all empty, but its pointer type's encoder writes
// a condition.
type fixedDoc struct {
	S string `bson:"s,omitempty"`
}

func (d *fixedDoc) MarshalBSON() ([]byte, error) {
	return bson.Marshal(bson.D{{Key: "kind", Value: "fixed"}})
}

// blankDoc has a Go field set, but its pointer type's encoder writes nothing.
type blankDoc struct{ S string }

func (d *blankDoc) MarshalBSON() ([]byte, error) { return bson.Marshal(bson.D{}) }

// A filter argument is encoded once, as given, and the filter carries those
// bytes: whether it is dropped as empty is decided by exactly what would be
// sent, so a scope or filter cannot be judged one way and sent another.
func TestFiltersCarryWhatWasJudged(t *testing.T) {
	cond := Match(&condEntry{Attempts: 1})
	for _, f := range []any{&blankDoc{S: "x"}, fixedDoc{}} {
		got, err := buildFilter(condEntryType, []Cond{cond}, f)
		if err != nil {
			t.Fatal(err)
		}
		alone, _ := buildFilter(condEntryType, []Cond{cond})
		if !sameDoc(got, alone) {
			t.Errorf("filter %#v encodes to nothing but was kept: %v", f, got)
		}
	}
	for _, f := range []any{&fixedDoc{}, blankDoc{S: "x"}} {
		want, err := bson.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := buildFilter(condEntryType, nil, f)
		if err != nil {
			t.Fatal(err)
		}
		if !sameDoc(got, bson.D{{Key: "$and", Value: bson.A{bson.Raw(want)}}}) {
			t.Errorf("filter %#v = %v; want the driver's own encoding %v", f, got, bson.Raw(want))
		}
		if c := Raw(f); c.err != nil || !bytes.Equal(c.raw.(bson.Raw), want) {
			t.Errorf("Raw(%#v) = %v, %v; want the driver's own encoding", f, c.raw, c.err)
		}
	}
	_, err := buildFilter(condEntryType, []Cond{Raw(&blankDoc{S: "x"})})
	wantInvalid(t, err)
	_, err = buildFilter(condEntryType, []Cond{Raw(fixedDoc{})})
	wantInvalid(t, err)
}

func TestMatchRefusesWhatItCannotCompare(t *testing.T) {
	type Empty struct{ hidden int }
	type withMap struct {
		Name  string            `bson:"name"`
		Extra map[string]string `bson:",inline"`
	}
	type withEmpty struct {
		Name string `bson:"name"`
		E    Empty  `bson:"e"`
	}
	_, err := buildFilter(reflect.TypeOf(withMap{}), []Cond{Match(&withMap{Name: "a", Extra: map[string]string{"k": "v"}})})
	wantInvalid(t, err)
	if !strings.Contains(err.Error(), "inlined map") {
		t.Errorf("error = %v; want it to name the inlined map", err)
	}
	_, err = buildFilter(reflect.TypeOf(withEmpty{}), []Cond{Match(&withEmpty{Name: "a", E: Empty{hidden: 1}})})
	wantInvalid(t, err)
	_, err = buildFilter(condEntryType, []Cond{Raw(&bson.D{})})
	wantInvalid(t, err)
	_, err = buildFilter(condEntryType, []Cond{Less("attempts", 3), MatchZero("attempts")})
	wantInvalid(t, err)
}

// A separate filter struct names fields of the entry by its own tags; values
// are compared by what they encode to, the numeric types with each other.
func TestMatchAcceptsASeparateFilterStruct(t *testing.T) {
	type jobFilter struct {
		Status   string `bson:"status"`
		Owner    string `bson:"spec.owner"`
		Attempts int32  `bson:"attempts"`
	}
	f, err := buildFilter(condEntryType, []Cond{Match(&jobFilter{Status: "running", Owner: "a", Attempts: 2})})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "status", Value: "running"}, {Key: "spec.owner", Value: "a"}, {Key: "attempts", Value: int32(2)}}
	if !sameDoc(f, want) {
		t.Errorf("filter = %v, want %v", f, want)
	}
}

// Changing the caller's struct after Match, even deep inside a slice, does
// not change the condition: values are encoded when it is built.
func TestMatchSnapshotsNestedReferences(t *testing.T) {
	type nested struct {
		Grid [][]string `bson:"grid"`
		Ptrs []*int64   `bson:"ptrs"`
	}
	n := int64(1)
	src := &nested{Grid: [][]string{{"a"}}, Ptrs: []*int64{&n}}
	c := Match(src)
	src.Grid[0][0], n = "changed", 99
	f, err := buildFilter(reflect.TypeOf(nested{}), []Cond{c})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "grid", Value: [][]string{{"a"}}}, {Key: "ptrs", Value: []int64{1}}}
	if !sameDoc(f, want) {
		t.Errorf("filter = %v, want the values as they were when Match was built", f)
	}
}

// A field hidden by an outer one hides its whole subtree: the encoder never
// writes it, so neither the index nor Match may name its children.
func TestHiddenSubtreesAreNotIndexed(t *testing.T) {
	type S struct {
		X int `bson:"x"`
	}
	type S2 struct {
		X int `bson:"x"`
		Y int `bson:"y"`
	}
	type Inner struct {
		Spec S2 `bson:"spec"`
	}
	type E struct {
		Inner `bson:",inline"`
		Spec  S `bson:"spec"`
	}
	idx := indexFor(reflect.TypeOf(E{}))
	if idx["spec.y"] != nil || idx["spec.x"] == nil {
		t.Fatalf("index = %v; want the outer spec.x and no spec.y", idx)
	}
	_, err := buildFilter(reflect.TypeOf(E{}), []Cond{Match(&E{Inner: Inner{Spec: S2{Y: 5}}})})
	wantInvalid(t, err)
}

// When two inlined fields tie for a name, an outer field of that name still
// wins, whatever the field order.
func TestAnOuterFieldBeatsATieBelowIt(t *testing.T) {
	type TA struct {
		Status string `bson:"status"`
	}
	type TB struct {
		Status string `bson:"status"`
	}
	type tie struct {
		TA     `bson:",inline"`
		TB     `bson:",inline"`
		Status string `bson:"status"`
		Owner  string `bson:"owner"`
	}
	tt := reflect.TypeOf(tie{})
	if indexFor(tt)["status"] == nil {
		t.Fatal("the outer status is missing from the index")
	}
	f, err := buildFilter(tt, []Cond{Match(&tie{Status: "running", Owner: "me"})})
	want := bson.D{{Key: "status", Value: "running"}, {Key: "owner", Value: "me"}}
	if err != nil || !sameDoc(f, want) {
		t.Errorf("filter = %v, %v; want %v", f, err, want)
	}
	// with no outer field, the tie hides the name altogether
	type tieOnly struct {
		TA `bson:",inline"`
		TB `bson:",inline"`
	}
	if indexFor(reflect.TypeOf(tieOnly{}))["status"] != nil {
		t.Error("a tied name was indexed")
	}
}

// Slices fit only when their elements do.
func TestSliceElementTypesMustFit(t *testing.T) {
	_, err := buildFilter(condEntryType, []Cond{Match(&struct {
		Tags []int `bson:"tags"`
	}{Tags: []int{1}})})
	wantInvalid(t, err)
	if _, err := buildFilter(condEntryType, []Cond{Match(&struct {
		Tags []string `bson:"tags"`
	}{Tags: []string{"a"}})}); err != nil {
		t.Errorf("[]string against []string was refused: %v", err)
	}
}

// A nil inlined pointer contributes nothing and does not panic.
func TestMatchSkipsANilInlinedPointer(t *testing.T) {
	type Extra struct {
		Zone string `bson:"zone"`
	}
	type withPtr struct {
		*Extra `bson:",inline"`
		Name   string `bson:"name"`
	}
	f, err := buildFilter(reflect.TypeOf(withPtr{}), []Cond{Match(&withPtr{Name: "a"})})
	if err != nil || !sameDoc(f, bson.D{{Key: "name", Value: "a"}}) {
		t.Errorf("filter = %v, %v", f, err)
	}
}

// inlineStatus has its own encoder, but inlined the driver ignores it and
// writes its fields into the outer document.
type inlineStatus struct {
	Status string `bson:"status,omitempty"`
}

func (s *inlineStatus) MarshalBSON() ([]byte, error) {
	return bson.Marshal(bson.D{{Key: "never", Value: "written"}})
}

// An inlined struct is flattened by its fields as the driver writes them,
// whatever encoder it has: a set field in it is a condition, never dropped.
func TestInlinedSelfEncodingStructIsFlattenedAsStored(t *testing.T) {
	type entry struct {
		Kind string       `bson:"kind"`
		S    inlineStatus `bson:",inline"`
	}
	stored, err := bson.Marshal(&entry{Kind: "job", S: inlineStatus{Status: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	if !sameDoc(bson.D{{Key: "kind", Value: "job"}, {Key: "status", Value: "done"}}, func() bson.D {
		var d bson.D
		_ = bson.Unmarshal(stored, &d)
		return d
	}()) {
		t.Fatalf("the driver stored %v; this test assumes it flattens the inlined struct", bson.Raw(stored))
	}
	f, err := buildFilter(reflect.TypeOf(entry{}), []Cond{Match(&entry{Kind: "job", S: inlineStatus{Status: "done"}})})
	if err != nil || !sameDoc(f, bson.D{{Key: "kind", Value: "job"}, {Key: "status", Value: "done"}}) {
		t.Errorf("Match = %v, %v; want both stored fields as conditions", f, err)
	}
	if _, err := buildFilter(reflect.TypeOf(entry{}), []Cond{In("status", "done")}); err != nil {
		t.Errorf("In on the inlined field was refused: %v", err)
	}
}

// A value that refers to itself is refused, not followed forever.
func TestSelfReferringValuesAreRefused(t *testing.T) {
	var a any
	a = &a
	done := make(chan Cond, 1)
	go func() { done <- In("attempts", a) }()
	select {
	case c := <-done:
		_, err := buildFilter(condEntryType, []Cond{c})
		wantInvalid(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("In on a value that refers to itself did not return")
	}
}

// The value walk runs only where a map could hide behind an interface: a
// type the static check already judges exactly is not walked value by value.
func TestValueWalkOnlyWhereAnInterfaceCanHide(t *testing.T) {
	for _, v := range []any{[]byte{}, []string{}, condSpec{}, time.Time{}, map[string]int{}} {
		if containsInterface(reflect.TypeOf(v), 0) {
			t.Errorf("containsInterface(%T) = true; its type already says whether it holds a map", v)
		}
	}
	for _, v := range []any{[]any{}, bson.D{}, condEntry{}, struct{ X []any }{}} {
		if !containsInterface(reflect.TypeOf(v), 0) {
			t.Errorf("containsInterface(%T) = false; a map can hide behind its interface", v)
		}
	}
}

// nest returns n lists, each holding the next.
func nest(n int) any {
	var v any = 1
	for i := 0; i < n; i++ {
		v = []any{v}
	}
	return v
}

// The options keep their own copies: changing what was passed to Where, If,
// WithIncrement or WithUnset after building the option changes nothing.
func TestOptionsKeepTheirOwnCopies(t *testing.T) {
	conds := []Cond{In("worker", "a")}
	where := Where(conds...)
	when := If(conds...)
	paths := []string{"worker"}
	unset := WithUnset(paths...)
	n := int64(1)
	inc := WithIncrement(&struct {
		N any `bson:"attempts"`
	}{N: &n})

	conds[0] = Raw(bson.D{{Key: "$comment", Value: "everything"}})
	paths[0] = "attempts"
	n = 1000

	var fo FindOptions
	where(&fo)
	got, err := BuildFilter[condEntry](fo.Conds()...)
	want, _ := BuildFilter[condEntry](In("worker", "a"))
	if err != nil || !sameDoc(got, want) {
		t.Errorf("Where = %v, %v; want the conditions it was given", got, err)
	}
	var uo updateOptions
	when(&uo)
	unset(&uo)
	inc(&uo)
	got, err = buildFilter(condEntryType, uo.conds)
	if err != nil || !sameDoc(got, want) {
		t.Errorf("If = %v, %v; want the conditions it was given", got, err)
	}
	if uo.unset[0] != "worker" {
		t.Errorf("WithUnset = %v; want the paths it was given", uo.unset)
	}
	if v := uo.increments[0].pairs[0].goValue; v != int64(1) {
		t.Errorf("WithIncrement value = %v; want the 1 it was given", v)
	}
}

// A map is refused wherever the value holds one, including behind the
// interfaces a check of the static type cannot see into; values that hold
// none are still compared.
func TestMatchRefusesAMapInAnInterfaceField(t *testing.T) {
	type withD struct {
		D bson.D `bson:"d"`
	}
	withMap := bson.M{"a": 1, "b": 2}
	for name, c := range map[string]Cond{
		"map in any":                    Match(&condEntry{Any: withMap}),
		"map in []any":                  Match(&condEntry{Any: []any{1, withMap}}),
		"map in bson.D in any":          Match(&condEntry{Any: bson.D{{Key: "x", Value: withMap}}}),
		"map in a struct value":         Match(&condEntry{Any: struct{ X any }{X: withMap}}),
		"map in a pointer":              Match(&condEntry{Any: &withMap}),
		"map behind a pointer in []any": Match(&condEntry{Any: []any{&withMap}}),
		"map in an array":               Match(&condEntry{Any: [2]any{1, withMap}}),
		"inlined map in a struct value": Match(&condEntry{Any: struct {
			M bson.M `bson:",inline"`
		}{M: withMap}}),
		"self-encoding value in []any": Match(&condEntry{Any: []any{fixedDoc{}}}),
		"nested past the bound":        Match(&condEntry{Any: nest(40)}),
	} {
		_, err := buildFilter(condEntryType, []Cond{c})
		wantInvalid(t, err)
		if err != nil && !strings.Contains(err.Error(), "map") {
			t.Errorf("%s: %v; want it refused for the map", name, err)
		}
	}
	if _, err := buildFilter(reflect.TypeOf(withD{}), []Cond{Match(&withD{D: bson.D{{Key: "x", Value: withMap}}})}); err == nil {
		t.Error("a map inside a bson.D field was compared")
	}
	for name, c := range map[string]Cond{
		"four nested lists":      Match(&condEntry{Any: []any{[]any{[]any{[]any{1}}}}}),
		"three nested bson.D":    Match(&condEntry{Any: bson.D{{Key: "a", Value: bson.D{{Key: "b", Value: bson.D{{Key: "c", Value: 1}}}}}}}),
		"dates in []any":         Match(&condEntry{Any: []any{time.Unix(1, 0).UTC()}}),
		"scalars in []any":       Match(&condEntry{Any: []any{1, "x"}}),
		"bson.D in any":          Match(&condEntry{Any: bson.D{{Key: "a", Value: 1}, {Key: "b", Value: 2}}}),
		"nested bson.D in []any": Match(&condEntry{Any: []any{bson.D{{Key: "a", Value: []any{1}}}}}),
	} {
		if _, err := buildFilter(condEntryType, []Cond{c}); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
	if _, err := buildFilter(reflect.TypeOf(withD{}), []Cond{Match(&withD{D: bson.D{{Key: "a", Value: 1}}})}); err != nil {
		t.Errorf("a bson.D field without a map was refused: %v", err)
	}
}

// An interface field in a filter struct is judged by the value it holds.
func TestAnInterfaceFilterFieldIsJudgedByItsValue(t *testing.T) {
	type entry struct {
		S string `bson:"s"`
	}
	if _, err := buildFilter(reflect.TypeOf(entry{}), []Cond{Match(&struct {
		S any `bson:"s"`
	}{S: "x"})}); err != nil {
		t.Errorf("a string held in an interface was refused: %v", err)
	}
}

// A Cond does not change after it is built, whatever happens to the inputs
// it was built from. Every constructor is here, so one added later that keeps
// a reference fails this test.
func TestNoConditionChangesAfterItIsBuilt(t *testing.T) {
	worker := "a"
	attempts := int64(3)
	entry := &condEntry{Worker: &worker, Tags: []string{"x"}, Spec: &condSpec{Owner: "o"}}
	paths := []string{"deleted"}
	values := []any{"queued", &worker}
	rawM := bson.M{"attempts": 1}
	rawD := bson.D{{Key: "ratio", Value: 0.5}}
	boxed := int64(5)
	var held any = &boxed
	rawB, err := bson.Marshal(bson.D{{Key: "worker", Value: "a"}})
	if err != nil {
		t.Fatal(err)
	}

	build := func() []Cond {
		return []Cond{
			Match(entry),
			MatchZero(paths...),
			In("worker", values...),
			NotIn("status", values[0]),
			Less("attempts", &attempts),
			LessEq("used", &attempts),
			Greater("ratio", &attempts),
			GreaterEq("at", time.Unix(5, 0).UTC()),
			Raw(rawM),
			Raw(rawD),
			Raw(bson.Raw(rawB)),
			In("attempts", &held),
		}
	}
	conds := build()
	// the filters are encoded now: a filter document can itself hold a
	// reference to an input, which would change along with it
	before := make([]string, len(conds))
	for i, c := range conds {
		f, err := buildFilter(condEntryType, []Cond{c})
		if err != nil {
			t.Fatalf("condition %d: %v", i, err)
		}
		b, err := bson.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = string(b)
	}

	worker, attempts = "changed", 99
	entry.Tags[0], entry.Spec.Owner, entry.Attempts = "changed", "changed", 7
	paths[0] = "worker"
	values[0] = "changed"
	clear(rawM)
	rawD[0].Value = 2.5
	boxed = 7
	copy(rawB[5:], "$comment") // the key and its terminator, in place: {$comment: "a"} would match every row

	for i, c := range conds {
		f, err := buildFilter(condEntryType, []Cond{c})
		if err != nil {
			t.Fatalf("condition %d: %v", i, err)
		}
		if b, _ := bson.Marshal(f); string(b) != before[i] {
			t.Errorf("condition %d changed after it was built: now %v", i, f)
		}
	}
}

// A Raw built from a document that is cleared afterwards still holds its
// condition: it cannot turn a delete into one of every row.
func TestRawIsSnapshotted(t *testing.T) {
	m := bson.M{"status": "done"}
	c := Raw(m)
	clear(m)
	f, err := buildFilter(condEntryType, []Cond{c})
	if err != nil || len(f) == 0 {
		t.Fatalf("filter = %v, %v; want the status condition", f, err)
	}
	if !strings.Contains(fmt.Sprint(f), "done") {
		t.Errorf("filter = %v; want it to still say status done", f)
	}
}

// ownerTag encodes itself as a string, through a pointer it holds.
type ownerTag struct{ Owner *string }

func (o ownerTag) MarshalBSONValue() (byte, []byte, error) {
	owner := ""
	if o.Owner != nil {
		owner = *o.Owner
	}
	t, b, err := bson.MarshalValue("owner:" + owner)
	return byte(t), b, err
}

// The two guards against a condition that matches everything each hold on
// their own: the zero value is no kind of condition, and any condition whose
// filter encodes to nothing is refused, whatever built it.
func TestEachEmptyConditionGuardHoldsAlone(t *testing.T) {
	_, err := buildFilter(condEntryType, []Cond{{}})
	wantInvalid(t, err)
	if !strings.Contains(err.Error(), "unknown condition") {
		t.Errorf("zero value: %v; want it refused as no kind of condition", err)
	}
	// a Match with no pairs cannot come from a constructor; it stands for
	// any future one that might yield an empty filter
	_, err = buildFilter(condEntryType, []Cond{{kind: condMatch}})
	wantInvalid(t, err)
	if !strings.Contains(err.Error(), "constrains nothing") {
		t.Errorf("empty element: %v; want the backstop's refusal", err)
	}
}
