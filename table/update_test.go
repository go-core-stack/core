// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

package table

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/utils"
)

// These tests run against the MongoDB the rest of this package's tests use.

type JobKey struct {
	Kind string `bson:"kind"`
	ID   string `bson:"id"`
}

type Job struct {
	Status    *string           `bson:"status,omitempty"`
	Worker    *string           `bson:"worker,omitempty"`
	Claim     *string           `bson:"claim,omitempty"`
	Attempts  int64             `bson:"attempts,omitempty"`
	NotBefore *int64            `bson:"notBefore,omitempty"`
	Used      *int64            `bson:"used,omitempty"`
	Deleted   bool              `bson:"deleted,omitempty"`
	Labels    map[string]string `bson:"labels,omitempty"`
	Note      *string           `bson:"note"` // no omitempty: a nil pointer is stored as null
	Spec      *JobSpec          `bson:"spec,omitempty"`
	Version   int64             `bson:"version,omitempty"`
	Rev       rev               `bson:"rev,omitempty"` // encodes itself, as a string
}

type JobSpec struct {
	Owner string `bson:"owner,omitempty"`
	Level int64  `bson:"level,omitempty"`
}

var (
	jobStoreOnce sync.Once
	jobStore     db.Store
)

// jobCollection returns a fresh, empty collection for one test.
func jobCollection(t *testing.T) db.StoreCollection {
	t.Helper()
	jobStoreOnce.Do(func() {
		client, err := db.NewMongoClient(&db.MongoConfig{Host: "localhost", Port: "27017", Username: "root", Password: "password"})
		if err != nil {
			log.Panicf("failed to connect to mongo DB Error: %s", err)
		}
		jobStore = client.GetDataStore("test")
	})
	// a fixed name per test, emptied before and after, so runs do not
	// accumulate collections
	name := "conditional-update-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	col := jobStore.GetCollection(name)
	_, _ = col.DeleteMany(context.Background(), bson.D{})
	t.Cleanup(func() { _, _ = col.DeleteMany(context.Background(), bson.D{}) })
	return col
}

func newJobTable(t *testing.T) *Table[JobKey, Job] {
	t.Helper()
	tbl := &Table[JobKey, Job]{}
	if err := tbl.Initialize(jobCollection(t)); err != nil {
		t.Fatal(err)
	}
	return tbl
}

var p = utils.Pointer[string]

func insertJob(t *testing.T, tbl interface {
	Insert(context.Context, *JobKey, *Job) error
}, id string, job *Job) *JobKey {
	t.Helper()
	key := &JobKey{Kind: "job", ID: id}
	if err := tbl.Insert(context.Background(), key, job); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestUpdateWithOpts_ClaimRaceHasOneWinner(t *testing.T) {
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "race", &Job{Status: p("queued")})

	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			won, err := tbl.UpdateWithOpts(context.Background(), key,
				&Job{Status: p("running"), Worker: p(fmt.Sprint(i))},
				If(In("status", "queued", "requeued")),
				WithIncrement(&Job{Attempts: 1}))
			if err != nil {
				t.Error(err)
			}
			if won {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims won, want exactly 1", wins.Load())
	}
	got, err := tbl.Find(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if *got.Status != "running" || got.Attempts != 1 {
		t.Errorf("row = %+v, want running with one attempt", got)
	}
}

func TestUpdateWithOpts_ATokenFencesAStaleOwner(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "fence", &Job{Status: p("queued")})

	claim := func(token string) bool {
		won, err := tbl.UpdateWithOpts(ctx, key, &Job{Status: p("running"), Claim: p(token)},
			If(In("status", "queued", "requeued")))
		if err != nil {
			t.Fatal(err)
		}
		return won
	}
	if !claim("first") {
		t.Fatal("first claim lost")
	}
	// timed out and requeued by a sweep that compares the claim it saw
	requeued, err := tbl.UpdateWithOpts(ctx, key, &Job{Status: p("requeued")},
		If(Match(&Job{Status: p("running"), Claim: p("first")})),
		WithUnset("claim", "worker"))
	if err != nil || !requeued {
		t.Fatalf("requeue = %v, %v", requeued, err)
	}
	if !claim("second") {
		t.Fatal("second claim lost")
	}
	// the first owner wakes up and tries to finish: refused
	done, err := tbl.UpdateWithOpts(ctx, key, &Job{Status: p("done")},
		If(Match(&Job{Status: p("running"), Claim: p("first")})))
	if err != nil || done {
		t.Fatalf("stale finish = %v, %v; want false", done, err)
	}
}

func TestUpdateWithOpts_FalseNotFoundAndApplied(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "states", &Job{Status: p("done")})
	absent := &JobKey{Kind: "job", ID: "absent"}

	if ok, err := tbl.UpdateWithOpts(ctx, key, &Job{Worker: p("a")}, If(In("status", "queued"))); ok || err != nil {
		t.Errorf("failed condition = %v, %v; want false, nil", ok, err)
	}
	if ok, err := tbl.UpdateWithOpts(ctx, absent, &Job{Worker: p("a")}, If(In("status", "queued"))); ok || err != nil {
		t.Errorf("absent row with a condition = %v, %v; want false, nil", ok, err)
	}
	if _, err := tbl.UpdateWithOpts(ctx, absent, &Job{Worker: p("a")}); !errors.IsNotFound(err) {
		t.Errorf("absent row without a condition = %v; want NotFound", err)
	}
	// the same values again: matched, unchanged, still applied
	if ok, err := tbl.UpdateWithOpts(ctx, key, &Job{Status: p("done")}); !ok || err != nil {
		t.Errorf("unchanged write = %v, %v; want true", ok, err)
	}
}

// MatchZero matches a field that is absent (omitempty at zero), stored as
// null (a nil pointer without omitempty) or stored as its zero value.
func TestUpdateWithOpts_MatchZeroSeesAbsentNullAndStoredZero(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	// "deleted" is absent and "note" is null on every row Insert writes
	fresh := insertJob(t, tbl, "fresh", &Job{Status: p("x")})
	marked := insertJob(t, tbl, "marked", &Job{Status: p("x"), Deleted: true})
	zero := insertJob(t, tbl, "zero", &Job{Status: p("x"), Note: p("")})

	if ok, err := tbl.UpdateWithOpts(ctx, fresh, &Job{Deleted: true}, If(MatchZero("deleted", "note"))); !ok || err != nil {
		t.Errorf("absent and null = %v, %v; want applied", ok, err)
	}
	if ok, err := tbl.UpdateWithOpts(ctx, zero, &Job{Worker: p("a")}, If(MatchZero("note"))); !ok || err != nil {
		t.Errorf("stored zero = %v, %v; want applied", ok, err)
	}
	if ok, err := tbl.UpdateWithOpts(ctx, marked, &Job{Deleted: true}, If(MatchZero("deleted"))); ok || err != nil {
		t.Errorf("already marked = %v, %v; want false", ok, err)
	}
}

// An entry that encodes to no fields, with nothing else to write, is refused.
func TestUpdateWithOpts_EmptyEntryIsRefused(t *testing.T) {
	type Sparse struct {
		Status *string `bson:"status,omitempty"`
	}
	tbl := &Table[JobKey, Sparse]{}
	if err := tbl.Initialize(jobCollection(t)); err != nil {
		t.Fatal(err)
	}
	key := &JobKey{Kind: "s", ID: "1"}
	if err := tbl.Insert(context.Background(), key, &Sparse{Status: p("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := tbl.UpdateWithOpts(context.Background(), key, &Sparse{}, If(In("status", "x"))); !errors.IsInvalidArgument(err) {
		t.Errorf("empty entry = %v; want InvalidArgument", err)
	}
}

func TestUpdateWithOpts_RangesAndAFloor(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "quota", &Job{Used: utils.Pointer[int64](5)})

	release := func(n int64) bool {
		ok, err := tbl.UpdateWithOpts(ctx, key, nil, If(GreaterEq("used", n)), WithIncrement(&Job{Used: utils.Pointer(-n)}))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !release(3) || release(3) || !release(2) {
		t.Fatal("a floor of zero was not kept")
	}
	got, _ := tbl.Find(ctx, key)
	if *got.Used != 0 {
		t.Errorf("used = %d, want 0", *got.Used)
	}
}

func TestUpdateWithOpts_RawIsAndedAndIfsCombine(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "raw", &Job{Status: p("running"), Attempts: 3})

	ok, err := tbl.UpdateWithOpts(ctx, key, &Job{Worker: p("a")},
		If(Match(&Job{Status: p("running")})),
		If(Raw(bson.M{"attempts": bson.M{"$gt": 5}})))
	if ok || err != nil {
		t.Errorf("a failing Raw in a second If = %v, %v; want false", ok, err)
	}
	ok, err = tbl.UpdateWithOpts(ctx, key, &Job{Worker: p("a")},
		If(Match(&Job{Status: p("running")}), Raw(bson.M{"attempts": bson.M{"$gt": 2}})))
	if !ok || err != nil {
		t.Errorf("a holding Raw = %v, %v; want true", ok, err)
	}
}

func TestUpdateWithOpts_IncrementAndUnset(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "inc", &Job{Status: p("running"), Worker: p("a"), Attempts: 2})

	ok, err := tbl.UpdateWithOpts(ctx, key, &Job{Status: p("requeued")},
		WithIncrement(&Job{Attempts: 1}), WithUnset("worker"))
	if !ok || err != nil {
		t.Fatalf("update = %v, %v", ok, err)
	}
	got, _ := tbl.Find(ctx, key)
	if *got.Status != "requeued" || got.Worker != nil || got.Attempts != 3 {
		t.Errorf("row = %+v, want requeued, no worker, 3 attempts", got)
	}
}

func TestUpdateWithOpts_Refusals(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "refuse", &Job{Status: p("running")})
	type Other struct {
		Tries int64 `bson:"tries"`
	}

	for name, call := range map[string]func() (bool, error){
		"If with no conditions": func() (bool, error) { return tbl.UpdateWithOpts(ctx, key, &Job{Worker: p("a")}, If()) },
		"unknown path":          func() (bool, error) { return tbl.UpdateWithOpts(ctx, key, &Job{Worker: p("a")}, If(In("stauts", "x"))) },
		"nothing to write":      func() (bool, error) { return tbl.UpdateWithOpts(ctx, key, nil, If(In("status", "running"))) },
		"increment of nothing":  func() (bool, error) { return tbl.UpdateWithOpts(ctx, key, nil, WithIncrement(&Job{})) },
		"increment of a string": func() (bool, error) { return tbl.UpdateWithOpts(ctx, key, nil, WithIncrement(&Job{Worker: p("a")})) },
		"increment of a path the entry lacks": func() (bool, error) {
			return tbl.UpdateWithOpts(ctx, key, nil, WithIncrement(&Other{Tries: 1}))
		},
		"unset an unknown path": func() (bool, error) { return tbl.UpdateWithOpts(ctx, key, nil, WithUnset("wroker")) },
		"set and increment the same path": func() (bool, error) {
			return tbl.UpdateWithOpts(ctx, key, &Job{Attempts: 1}, WithIncrement(&Job{Attempts: 1}))
		},
		"increment and unset the same path": func() (bool, error) {
			return tbl.UpdateWithOpts(ctx, key, nil, WithIncrement(&Job{Attempts: 1}), WithUnset("attempts"))
		},
		"Match on a map": func() (bool, error) {
			return tbl.UpdateWithOpts(ctx, key, &Job{Worker: p("a")}, If(Match(&Job{Labels: map[string]string{"a": "b"}})))
		},
	} {
		t.Run(name, func(t *testing.T) {
			ok, err := call()
			if ok || err == nil || !errors.IsInvalidArgument(err) {
				t.Fatalf("= %v, %v; want InvalidArgument", ok, err)
			}
		})
	}
}

// An overlap is refused by the table, before anything reaches the server
// (which would refuse it too, with a less helpful message).
func TestUpdateWithOpts_OverlapIsRefusedBeforeSending(t *testing.T) {
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "overlap", &Job{Status: p("running")})
	_, err := tbl.UpdateWithOpts(context.Background(), key, nil, WithIncrement(&Job{Attempts: 1}), WithUnset("attempts"))
	if !errors.IsInvalidArgument(err) || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap = %v; want the table's own InvalidArgument", err)
	}
}

// renamedJob is stored by its own encoder, which writes its status under
// another name: its Go fields do not describe what is stored.
type renamedJob struct {
	Status string `bson:"status"`
	Tries  int64  `bson:"tries"`
}

func (r *renamedJob) MarshalBSON() ([]byte, error) {
	return bson.Marshal(bson.D{{Key: "phase", Value: r.Status}, {Key: "tries", Value: r.Tries}})
}

// An entry type that encodes itself is reached only through Raw: every typed
// path is refused before anything is sent, so a NotIn on a field that is
// never stored cannot delete the rows it was meant to keep. Raw and plain
// entry writes still work.
func TestSelfEncodingEntryTypeIsReachedThroughRaw(t *testing.T) {
	ctx := context.Background()
	tbl := &Table[JobKey, renamedJob]{}
	if err := tbl.Initialize(jobCollection(t)); err != nil {
		t.Fatal(err)
	}
	key := &JobKey{Kind: "job", ID: "renamed"}
	if err := tbl.Insert(ctx, key, &renamedJob{Status: "done", Tries: 1}); err != nil {
		t.Fatal(err)
	}

	refused := map[string]func() error{
		"DeleteWhere NotIn": func() error { _, err := tbl.DeleteWhere(ctx, NotIn("status", "done")); return err },
		"CountWhere In":     func() error { _, err := tbl.CountWhere(ctx, In("status", "done")); return err },
		"CountWhere Match":  func() error { _, err := tbl.CountWhere(ctx, Match(&renamedJob{Status: "done"})); return err },
		"CountWhere MatchZero": func() error {
			_, err := tbl.CountWhere(ctx, MatchZero("status"))
			return err
		},
		"CountWhere range": func() error { _, err := tbl.CountWhere(ctx, Less("tries", 5)); return err },
		"If": func() error {
			_, err := tbl.UpdateWithOpts(ctx, key, &renamedJob{Status: "x"}, If(In("status", "done")))
			return err
		},
		"WithIncrement": func() error {
			_, err := tbl.UpdateWithOpts(ctx, key, nil, WithIncrement(&renamedJob{Tries: 1}))
			return err
		},
		"WithUnset": func() error {
			_, err := tbl.UpdateWithOpts(ctx, key, &renamedJob{Status: "x"}, WithUnset("status"))
			return err
		},
	}
	for name, call := range refused {
		err := call()
		if !errors.IsInvalidArgument(err) || !strings.Contains(err.Error(), "use Raw") {
			t.Errorf("%s = %v; want InvalidArgument pointing to Raw", name, err)
		}
	}

	if n, err := tbl.CountWhere(ctx, Raw(bson.D{{Key: "phase", Value: "done"}})); err != nil || n != 1 {
		t.Fatalf("CountWhere(Raw) = %d, %v; want the one stored row", n, err)
	}
	if ok, err := tbl.UpdateWithOpts(ctx, key, &renamedJob{Status: "queued", Tries: 2}); err != nil || !ok {
		t.Fatalf("plain UpdateWithOpts = %v, %v; want it applied", ok, err)
	}
	if n, err := tbl.CountWhere(ctx, Raw(bson.D{{Key: "phase", Value: "queued"}})); err != nil || n != 1 {
		t.Errorf("after the update, CountWhere(Raw) = %d, %v; want the row as its encoder wrote it", n, err)
	}
}

// So is an increment of a field whose type encodes itself: what it stores
// need not be a number however its Go type looks.
func TestUpdateWithOpts_SelfEncodingIncrementIsRefusedBeforeSending(t *testing.T) {
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "self-encoding", &Job{Status: p("running")})
	_, err := tbl.UpdateWithOpts(context.Background(), key, nil, WithIncrement(&Job{Rev: 1}))
	if !errors.IsInvalidArgument(err) || !strings.Contains(err.Error(), "use Raw") {
		t.Fatalf("increment of a self-encoding field = %v; want InvalidArgument pointing to Raw", err)
	}
	// and of a plain field by a value whose type encodes itself: its Go
	// value is not what it would encode to
	_, err = tbl.UpdateWithOpts(context.Background(), key, nil, WithIncrement(&struct {
		Attempts scaled `bson:"attempts"`
	}{Attempts: 1}))
	if !errors.IsInvalidArgument(err) || !strings.Contains(err.Error(), "use Raw") {
		t.Fatalf("increment by a self-encoding value = %v; want InvalidArgument pointing to Raw", err)
	}
}

// So is an increment of a field that is not numeric.
func TestUpdateWithOpts_NonNumericIncrementIsRefusedBeforeSending(t *testing.T) {
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "numeric", &Job{Status: p("running")})
	_, err := tbl.UpdateWithOpts(context.Background(), key, nil, WithIncrement(&Job{Worker: p("a")}))
	if !errors.IsInvalidArgument(err) || !strings.Contains(err.Error(), "not a numeric field") {
		t.Fatalf("increment of a string = %v; want the table's own InvalidArgument", err)
	}
}

// $inc on a field stored as null is a server write error; it comes back as
// InvalidArgument, not as an unclassified driver error.
func TestUpdateWithOpts_ServerTypeMismatchIsInvalidArgument(t *testing.T) {
	ctx := context.Background()
	type Counter struct {
		N *int64 `bson:"n"` // no omitempty: nil is stored as null
	}
	tbl := &Table[JobKey, Counter]{}
	if err := tbl.Initialize(jobCollection(t)); err != nil {
		t.Fatal(err)
	}
	key := &JobKey{Kind: "c", ID: "1"}
	if err := tbl.Insert(ctx, key, &Counter{}); err != nil {
		t.Fatal(err)
	}
	_, err := tbl.UpdateWithOpts(ctx, key, nil, WithIncrement(&Counter{N: utils.Pointer[int64](1)}))
	if !errors.IsInvalidArgument(err) {
		t.Fatalf("$inc on null = %v; want InvalidArgument", err)
	}
}

func TestWhereCountWhereDeleteWhere(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	for i, st := range []string{"queued", "queued", "requeued", "running", "done"} {
		insertJob(t, tbl, fmt.Sprint(i), &Job{Status: p(st), Attempts: int64(i)})
	}

	for name, filter := range map[string]any{"nil": nil, "bson.M": bson.M{"attempts": bson.M{"$lt": 3}}, "bson.D": bson.D{{Key: "attempts", Value: bson.D{{Key: "$lt", Value: 3}}}}} {
		got, err := tbl.FindManyWithOpts(ctx, filter, Where(In("status", "queued", "requeued")),
			WithSort(SortOption{Field: "attempts", Direction: SortDescending}), WithLimit(10))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := 3
		if filter != nil {
			want = 2
		}
		if len(got) != want {
			t.Errorf("%s filter: %d rows, want %d", name, len(got), want)
		}
	}
	if _, err := tbl.FindManyWithOpts(ctx, nil, Where()); !errors.IsInvalidArgument(err) {
		t.Errorf("empty Where = %v; want InvalidArgument", err)
	}
	if n, err := tbl.CountWhere(ctx, NotIn("status", "done")); n != 4 || err != nil {
		t.Errorf("CountWhere = %d, %v; want 4", n, err)
	}
	if n, err := tbl.CountWhere(ctx); n != 5 || err != nil {
		t.Errorf("CountWhere() = %d, %v; want 5", n, err)
	}
	if _, err := tbl.DeleteWhere(ctx); !errors.IsInvalidArgument(err) {
		t.Errorf("DeleteWhere() = %v; want InvalidArgument", err)
	}
	if n, err := tbl.DeleteWhere(ctx, In("status", "nothing-like-this")); n != 0 || err != nil {
		t.Errorf("DeleteWhere of nothing = %d, %v; want 0, nil", n, err)
	}
	if n, err := tbl.DeleteWhere(ctx, Greater("attempts", 2)); n != 2 || err != nil {
		t.Errorf("DeleteWhere = %d, %v; want 2", n, err)
	}
}

// A cached table scoped to part of a shared collection counts, finds,
// deletes and updates only its own rows.
func TestCachedTableTypedMethodsStayInScope(t *testing.T) {
	ctx := context.Background()
	col := jobCollection(t)
	scoped := &CachedTable[JobKey, Job]{}
	if err := scoped.InitializeWithConfig(col, WithFilter(bson.M{"_id.kind": "mine"})); err != nil {
		t.Fatal(err)
	}
	mine := &JobKey{Kind: "mine", ID: "1"}
	theirs := &JobKey{Kind: "theirs", ID: "1"}
	for _, k := range []*JobKey{mine, theirs} {
		if err := scoped.Insert(ctx, k, &Job{Status: p("done")}); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := scoped.CountWhere(ctx); n != 1 || err != nil {
		t.Errorf("CountWhere() = %d, %v; want 1", n, err)
	}
	if n, err := scoped.CountWhere(ctx, In("status", "done")); n != 1 || err != nil {
		t.Errorf("CountWhere = %d, %v; want 1", n, err)
	}
	// finds are not scoped, with or without Where; Raw(scope) narrows them
	if got, err := scoped.DBFindManyWithOpts(ctx, nil); len(got) != 2 || err != nil {
		t.Errorf("DBFindManyWithOpts = %d rows, %v; want 2 (unscoped)", len(got), err)
	}
	if got, err := scoped.DBFindManyWithOpts(ctx, nil, Where(In("status", "done"))); len(got) != 2 || err != nil {
		t.Errorf("DBFindManyWithOpts with Where = %d rows, %v; want 2 (unscoped)", len(got), err)
	}
	if got, err := scoped.DBFindManyWithOpts(ctx, nil, Where(In("status", "done"), Raw(bson.M{"_id.kind": "mine"}))); len(got) != 1 || err != nil {
		t.Errorf("DBFindManyWithOpts with Where and the scope as Raw = %d rows, %v; want 1", len(got), err)
	}
	if ok, err := scoped.UpdateWithOpts(ctx, theirs, &Job{Worker: p("a")}, If(In("status", "done"))); ok || err != nil {
		t.Errorf("update outside scope = %v, %v; want false", ok, err)
	}
	if n, err := scoped.DeleteWhere(ctx, In("status", "done")); n != 1 || err != nil {
		t.Errorf("DeleteWhere = %d, %v; want 1", n, err)
	}
	if n, _ := col.Count(ctx, bson.M{"_id.kind": "theirs"}); n != 1 {
		t.Error("a row outside the table's scope was deleted")
	}
}

func TestUpdateWithOpts_SeveralIfsAllHold(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "ifs", &Job{Status: p("running"), Attempts: 3})
	// the first If fails and the second holds: the update must not apply
	ok, err := tbl.UpdateWithOpts(ctx, key, &Job{Worker: p("a")},
		If(In("status", "queued")), If(Raw(bson.M{"attempts": bson.M{"$gt": 2}})))
	if ok || err != nil {
		t.Fatalf("= %v, %v; want false: every If must hold", ok, err)
	}
}

func TestUpdateWithOpts_NestedPathsAndParentChildOverlap(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "nested", &Job{Spec: &JobSpec{Owner: "a", Level: 1}})

	if ok, err := tbl.UpdateWithOpts(ctx, key, &Job{Worker: p("w")}, If(Match(&Job{Spec: &JobSpec{Owner: "b"}}))); ok || err != nil {
		t.Errorf("nested mismatch = %v, %v; want false", ok, err)
	}
	if ok, err := tbl.UpdateWithOpts(ctx, key, nil, If(Match(&Job{Spec: &JobSpec{Owner: "a"}})), WithIncrement(&Job{Spec: &JobSpec{Level: 2}})); !ok || err != nil {
		t.Errorf("nested match and increment = %v, %v; want applied", ok, err)
	}
	got, _ := tbl.Find(ctx, key)
	if got.Spec.Level != 3 || got.Spec.Owner != "a" {
		t.Errorf("spec = %+v, want owner a at level 3", got.Spec)
	}
	_, err := tbl.UpdateWithOpts(ctx, key, &Job{Spec: &JobSpec{Owner: "c"}}, WithIncrement(&Job{Spec: &JobSpec{Level: 1}}))
	if !errors.IsInvalidArgument(err) || !strings.Contains(err.Error(), "overlaps") {
		t.Errorf("parent set with child incremented = %v; want the table's overlap refusal", err)
	}
}

// The version pattern: rows start at 1; a row without the field is brought
// in once with MatchZero; every writer compares and bumps it.
func TestUpdateWithOpts_VersionPattern(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	legacy := insertJob(t, tbl, "legacy", &Job{Status: p("x")})
	next := &Job{Status: p("x"), Version: 1}
	if ok, err := tbl.UpdateWithOpts(ctx, legacy, next, If(MatchZero("version"))); !ok || err != nil {
		t.Fatalf("bringing in a row without a version = %v, %v", ok, err)
	}

	write := func(seen int64, owner string) bool {
		cur, err := tbl.Find(ctx, legacy)
		if err != nil {
			t.Fatal(err)
		}
		next := *cur
		next.Worker = p(owner)
		next.Version = 0 // omitempty: left out of $set, bumped by the increment
		ok, err := tbl.UpdateWithOpts(ctx, legacy, &next, If(Match(&Job{Version: seen})), WithIncrement(&Job{Version: 1}))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !write(1, "a") {
		t.Fatal("first versioned write lost")
	}
	if write(1, "b") {
		t.Fatal("a write based on version 1 applied after version 2 was written")
	}
	got, _ := tbl.Find(ctx, legacy)
	if got.Version != 2 || *got.Worker != "a" {
		t.Errorf("row = version %d worker %s; want 2, a", got.Version, *got.Worker)
	}
}

func TestConditionsOnAbsentFields(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	insertJob(t, tbl, "with", &Job{Status: p("x"), Attempts: 2})
	insertJob(t, tbl, "without", &Job{Status: p("x")})

	if n, err := tbl.CountWhere(ctx, NotIn("worker", "a")); n != 2 || err != nil {
		t.Errorf("NotIn over an absent field = %d, %v; want 2: absent rows match", n, err)
	}
	if n, err := tbl.CountWhere(ctx, Less("used", 10)); n != 0 || err != nil {
		t.Errorf("a range over an absent field = %d, %v; want 0", n, err)
	}
	if n, err := tbl.CountWhere(ctx, In("attempts", 0)); n != 1 || err != nil {
		t.Errorf("In with a zero value = %d, %v; want 1: the absent row", n, err)
	}
}

func TestWhereWithOffsetAndBuildFilterAgree(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	for i := 0; i < 5; i++ {
		insertJob(t, tbl, fmt.Sprint(i), &Job{Status: p("queued"), Attempts: int64(i + 1)})
	}
	conds := []Cond{In("status", "queued"), Greater("attempts", 1)}
	got, err := tbl.FindManyWithOpts(ctx, nil, Where(conds...),
		WithSort(SortOption{Field: "attempts", Direction: SortAscending}), WithOffset(1), WithLimit(2))
	if err != nil || len(got) != 2 || got[0].Attempts != 3 {
		t.Fatalf("Where with offset = %d rows (%v); want attempts 3 and 4", len(got), err)
	}
	f, err := BuildFilter[Job](conds...)
	if err != nil {
		t.Fatal(err)
	}
	viaFilter, err := tbl.FindManyWithOpts(ctx, f)
	viaWhere, err2 := tbl.FindManyWithOpts(ctx, nil, Where(conds...))
	if err != nil || err2 != nil || len(viaFilter) != 4 || len(viaWhere) != 4 {
		t.Errorf("BuildFilter selects %d rows, Where selects %d; want the same 4", len(viaFilter), len(viaWhere))
	}
}

// A cached table's entry follows a conditional update through the change
// stream, as it does for Update.
func TestCachedTableFollowsAConditionalUpdate(t *testing.T) {
	ctx := context.Background()
	tbl := &CachedTable[JobKey, Job]{}
	if err := tbl.Initialize(jobCollection(t)); err != nil {
		t.Fatal(err)
	}
	key := &JobKey{Kind: "job", ID: "cached"}
	if err := tbl.Insert(ctx, key, &Job{Status: p("queued")}); err != nil {
		t.Fatal(err)
	}
	if ok, err := tbl.UpdateWithOpts(ctx, key, &Job{Status: p("running")}, If(In("status", "queued"))); !ok || err != nil {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := tbl.Find(ctx, key)
		if err == nil && got.Status != nil && *got.Status == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache still has %+v, %v", got, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A table whose entry type the index cannot fully describe still starts.
func TestUnusualEntryTypeInitializes(t *testing.T) {
	type Unusual struct {
		Any  any               `bson:"any,omitempty"`
		Raw  bson.Raw          `bson:"raw,omitempty"`
		M    map[string][]any  `bson:"m,omitempty"`
		Next *Unusual          `bson:"next,omitempty"`
		Opt  map[string]string `bson:",inline"`
	}
	tbl := &Table[JobKey, Unusual]{}
	if err := tbl.Initialize(jobCollection(t)); err != nil {
		t.Fatalf("Initialize = %v", err)
	}
}

// The key cannot be unset, even through a field that maps to it.
func TestUpdateWithOpts_KeyCannotBeUnset(t *testing.T) {
	type Keyed struct {
		ID   *JobKey `bson:"_id,omitempty"`
		Name string  `bson:"name,omitempty"`
	}
	tbl := &Table[JobKey, Keyed]{}
	if err := tbl.Initialize(jobCollection(t)); err != nil {
		t.Fatal(err)
	}
	key := &JobKey{Kind: "k", ID: "1"}
	if err := tbl.Insert(context.Background(), key, &Keyed{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	_, err := tbl.UpdateWithOpts(context.Background(), key, nil, WithUnset("_id"))
	if !errors.IsInvalidArgument(err) || !strings.Contains(err.Error(), "key cannot be changed") {
		t.Fatalf("unset _id = %v; want the table's refusal", err)
	}
}

// A separate filter struct and a separate counters struct work end to end,
// a narrower numeric type included.
func TestUpdateWithOpts_SeparateFilterAndCounterStructs(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "filters", &Job{Status: p("running"), Attempts: 2, Spec: &JobSpec{Owner: "a"}})
	type running struct {
		Status string `bson:"status"`
		Owner  string `bson:"spec.owner"`
	}
	type counters struct {
		Attempts int32 `bson:"attempts"`
	}
	ok, err := tbl.UpdateWithOpts(ctx, key, nil,
		If(Match(&running{Status: "running", Owner: "a"})), WithIncrement(&counters{Attempts: 1}))
	if !ok || err != nil {
		t.Fatalf("= %v, %v; want applied", ok, err)
	}
	got, _ := tbl.Find(ctx, key)
	if got.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", got.Attempts)
	}
	if n, err := tbl.CountWhere(ctx, Match(&counters{Attempts: 3})); n != 1 || err != nil {
		t.Errorf("an int32 filter on an int64 field = %d, %v; want 1", n, err)
	}
}

// An increment value must be a number, whatever the field it names.
func TestUpdateWithOpts_NonNumericIncrementValueIsRefused(t *testing.T) {
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "nonnumeric", &Job{Attempts: 1})
	type wrong struct {
		Attempts string `bson:"attempts"`
	}
	_, err := tbl.UpdateWithOpts(context.Background(), key, nil, WithIncrement(&wrong{Attempts: "1"}))
	if !errors.IsInvalidArgument(err) || !strings.Contains(err.Error(), "not a numeric field and value") {
		t.Fatalf("= %v; want the table's own refusal", err)
	}
}

// An increment is converted to the field's own type: a fraction into an
// integer field is refused rather than turning the field into a double.
func TestUpdateWithOpts_IncrementKeepsTheFieldType(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "typed", &Job{Attempts: 1})
	type half struct {
		Attempts float64 `bson:"attempts"`
	}
	if _, err := tbl.UpdateWithOpts(ctx, key, nil, WithIncrement(&half{Attempts: 0.5})); !errors.IsInvalidArgument(err) {
		t.Fatalf("a fraction into an int64 field = %v; want InvalidArgument", err)
	}
	if ok, err := tbl.UpdateWithOpts(ctx, key, nil, WithIncrement(&half{Attempts: 2})); !ok || err != nil {
		t.Fatalf("a whole float = %v, %v; want applied", ok, err)
	}
	got, err := tbl.Find(ctx, key)
	if err != nil || got.Attempts != 3 {
		t.Fatalf("row = %+v, %v; want attempts 3, still readable as int64", got, err)
	}
}

// A Raw that encodes to nothing cannot turn DeleteWhere into a delete of
// every row.
func TestDeleteWhereRefusesARawThatEncodesToNothing(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	insertJob(t, tbl, "keep", &Job{Status: p("done")})
	type query struct {
		Status string `bson:"status,omitempty"`
	}
	if _, err := tbl.DeleteWhere(ctx, Raw(&query{})); !errors.IsInvalidArgument(err) {
		t.Fatalf("= %v; want InvalidArgument", err)
	}
	if n, _ := tbl.CountWhere(ctx); n != 1 {
		t.Fatalf("%d rows left, want 1", n)
	}
}

// A nil option is refused rather than skipped: skipping a nil If would make
// a guarded update unconditional.
func TestUpdateWithOpts_NilOptionIsRefused(t *testing.T) {
	tbl := newJobTable(t)
	key := insertJob(t, tbl, "nilopt", &Job{Status: p("done")})
	var guard UpdateOption
	if ok, err := tbl.UpdateWithOpts(context.Background(), key, &Job{Worker: p("a")}, guard); ok || !errors.IsInvalidArgument(err) {
		t.Fatalf("= %v, %v; want InvalidArgument", ok, err)
	}
}

// A zero-value Cond, the kind an unset variable or slice slot holds, is
// refused, not taken as a condition that matches every row.
func TestDeleteWhereRefusesAZeroValueCond(t *testing.T) {
	ctx := context.Background()
	tbl := newJobTable(t)
	insertJob(t, tbl, "keep", &Job{Status: p("done")})
	conds := make([]Cond, 1) // an unfilled slot
	if _, err := tbl.DeleteWhere(ctx, conds...); !errors.IsInvalidArgument(err) {
		t.Fatalf("= %v; want InvalidArgument", err)
	}
	if ok, err := tbl.UpdateWithOpts(ctx, &JobKey{Kind: "job", ID: "keep"}, &Job{Worker: p("a")}, If(Cond{})); ok || !errors.IsInvalidArgument(err) {
		t.Fatalf("If(Cond{}) = %v, %v; want InvalidArgument", ok, err)
	}
	if n, _ := tbl.CountWhere(ctx); n != 1 {
		t.Fatalf("%d rows left, want 1", n)
	}
}
