// This utility reseeds a target MongoDB database from a source replica set, then
// tails the change stream to reconcile new writes so the two stay in sync.
// go run experimental/replicate_mongo.go -src mongodb://root:password@localhost:27017/?replicaSet=rs0 -dst mongodb://root:password@192.168.100.21:27016/ -srcdb auth-gateway -dstdb auth-gateway -keep true
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"runtime"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
)

// main orchestrates the reseed workflow: capture a change-stream bookmark,
// copy each collection, then replay queued changes until the stream is quiet
// (or indefinitely when keepRun is set).
func main() {
	var (
		srcURI   = flag.String("src", "mongodb://srcUser:srcPass@src-hosts/?replicaSet=rs0", "source MongoDB URI")
		dstURI   = flag.String("dst", "mongodb://dstUser:dstPass@dst-hosts/?replicaSet=rs1", "target MongoDB URI")
		srcDB    = flag.String("srcdb", "source_db", "source database name")
		dstDB    = flag.String("dstdb", "target_db", "target database name")
		drop     = flag.Bool("drop", true, "drop target collections before insert")
		workers  = flag.Int("workers", max(2, runtime.NumCPU()/2), "parallel collection copy workers")
		pageSize = flag.Int("pagesize", 2000, "find() batch size during copy")
		drainSec = flag.Int("drainGrace", 10, "grace seconds with no events before exiting (one-shot reseed)")
		keepRun  = flag.Bool("keep", false, "keep tailing after reseed (continuous)")
	)
	flag.Parse()

	ctx := context.Background()
	src, err := mongo.Connect(options.Client().ApplyURI(*srcURI).SetRetryReads(true))
	must(err)
	defer src.Disconnect(ctx)

	dst, err := mongo.Connect(options.Client().ApplyURI(*dstURI).SetRetryWrites(true))
	must(err)
	defer dst.Disconnect(ctx)

	// 1) Open a DB-scope change stream to bookmark "now".
	srcDBHandle := src.Database(*srcDB, options.Database().SetReadConcern(readconcern.Majority()))
	dstDBHandle := dst.Database(*dstDB)

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "operationType", Value: bson.D{{Key: "$in", Value: bson.A{"insert", "update", "replace", "delete"}}}},
		}}},
	}
	csOpts := options.ChangeStream().
		SetFullDocument(options.UpdateLookup).
		SetBatchSize(500).
		SetMaxAwaitTime(5 * time.Second)

	stream, err := srcDBHandle.Watch(ctx, pipeline, csOpts)
	must(err)
	defer stream.Close(ctx)

	// Force a resume token / operation time as a bookmark.
	_ = stream.TryNext(ctx)
	startToken := stream.ResumeToken()
	var startAt *bson.Timestamp
	if len(startToken) == 0 && stream.Current != nil {
		// Fallback to the stream's operation time if token is empty.
		// The driver v2 exposes cluster times via change stream tokens/metadata internally; using a new stream with StartAtOperationTime is fine.
		if t, i, ok := stream.Current.Lookup("_id").Document().Lookup("$clusterTime").TimestampOK(); ok {
			startAt = &bson.Timestamp{T: t, I: i}
		}
	}

	log.Println("Bookmark captured; starting full copy...")

	// 2) Copy all collections (in parallel).
	must(copyAllCollections(ctx, srcDBHandle, dstDBHandle, *drop, *pageSize, *workers))

	// 3) Reopen from bookmark and catch up.
	if len(startToken) > 0 {
		csOpts.SetResumeAfter(startToken)
	} else if startAt != nil && !startAt.IsZero() {
		csOpts.SetStartAtOperationTime(startAt) // v2 uses bson.Timestamp
	}
	_ = stream.Close(ctx)
	stream, err = srcDBHandle.Watch(ctx, pipeline, csOpts)
	must(err)
	defer stream.Close(ctx)

	log.Println("Replaying queued changes...")
	if err := drainChanges(ctx, stream, dstDBHandle, time.Duration(*drainSec)*time.Second, *keepRun); err != nil {
		log.Fatalf("drain failed: %v", err)
	}
	log.Println("Reseed complete.")
}

// copyAllCollections fans out copyOneCollection jobs across a worker pool so
// that large datasets are transferred in parallel while shielding callers from
// collection-level failures.
func copyAllCollections(ctx context.Context, srcDB, dstDB *mongo.Database, drop bool, pageSize, workers int) error {
	names, err := srcDB.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return err
	}
	// Skip system & internal collections.
	var cols []string
	for _, n := range names {
		if strings.HasPrefix(n, "system.") || n == "system.profile" {
			continue
		}
		cols = append(cols, n)
	}
	type job struct{ name string }
	jobs := make(chan job, len(cols))
	errs := make(chan error, workers)

	worker := func() {
		for j := range jobs {
			if err := copyOneCollection(ctx, srcDB.Collection(j.name), dstDB.Collection(j.name), drop, pageSize); err != nil {
				errs <- errors.New("copy " + j.name + ": " + err.Error())
				return
			}
		}
		errs <- nil
	}

	for i := 0; i < workers; i++ {
		go worker()
	}
	for _, c := range cols {
		jobs <- job{name: c}
	}
	close(jobs)

	// Wait for workers.
	var firstErr error
	for i := 0; i < workers; i++ {
		if e := <-errs; e != nil && firstErr == nil {
			firstErr = e
		}
	}
	return firstErr
}

// copyOneCollection streams documents from the source collection and upserts
// them into the destination in bulk batches to keep memory usage predictable.
func copyOneCollection(ctx context.Context, srcColl, dstColl *mongo.Collection, drop bool, pageSize int) error {
	if drop {
		_ = dstColl.Drop(ctx)
	}
	cur, err := srcColl.Find(ctx, bson.D{}, options.Find().
		SetBatchSize(int32(pageSize)).
		SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	models := make([]mongo.WriteModel, 0, 5000)
	flush := func() error {
		if len(models) == 0 {
			return nil
		}
		_, err := dstColl.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
		models = models[:0]
		return err
	}

	for cur.Next(ctx) {
		raw := cur.Current
		idVal := raw.Lookup("_id")
		/*
			// Decode raw into a bson.D
			var doc bson.D
			if err := bson.Unmarshal(raw, &doc); err != nil {
				return err
			}

			// Remove _id from doc
			filtered := make(bson.D, 0, len(doc)-1)
			for _, elem := range doc {
				if elem.Key != "_id" {
					filtered = append(filtered, elem)
				}
			}
		*/
		var idDoc bson.D
		if err := bson.Unmarshal(idVal.Value, &idDoc); err != nil {
			return err
		}
		models = append(models, mongo.NewReplaceOneModel().
			SetFilter(bson.D{{Key: "_id", Value: idDoc}}).
			//SetReplacement(filtered).
			SetReplacement(raw).
			SetUpsert(true))
		if len(models) >= 5000 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := cur.Err(); err != nil {
		return err
	}
	return flush()
}

// changeEvent mirrors the subset of change stream fields we need to apply
// operations to the destination database.
type changeEvent struct {
	OperationType string `bson:"operationType"`
	FullDocument  bson.M `bson:"fullDocument"`
	Ns            struct {
		DB   string `bson:"db"`
		Coll string `bson:"coll"`
	} `bson:"ns"`
	DocumentKey       bson.M `bson:"documentKey"`
	UpdateDescription struct {
		UpdatedFields bson.M   `bson:"updatedFields"`
		RemovedFields []string `bson:"removedFields"`
	} `bson:"updateDescription"`
}

// drainChanges replays change-stream events into the destination database until
// the stream is quiet for the requested grace period (one-shot) or forever
// (continuous mode).
func drainChanges(ctx context.Context, stream *mongo.ChangeStream, dstDB *mongo.Database, quiet time.Duration, keep bool) error {
	idle := time.NewTimer(quiet)
	defer idle.Stop()

	for {
		// Non-blocking poll
		if !stream.TryNext(ctx) {
			if err := stream.Err(); err != nil {
				// Detect history loss (oplog rolled over) and abort; caller should reseed again.
				var cmdErr mongo.CommandError
				if errors.As(err, &cmdErr) && (cmdErr.Name == "ChangeStreamHistoryLost" || cmdErr.Code == 286) {
					return err
				}
				return err
			}
			// Graceful exit if no events for 'quiet' duration and not running continuously.
			if !keep {
				select {
				case <-idle.C:
					return nil
				default:
				}
			}
			time.Sleep(150 * time.Millisecond)
			continue
		}

		// Got an event; reset idle timer.
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(quiet)

		var ev changeEvent
		if err := stream.Decode(&ev); err != nil {
			log.Printf("decode: %v", err)
			continue
		}

		coll := dstDB.Collection(ev.Ns.Coll)
		switch ev.OperationType {
		case "insert", "replace":
			if _, err := coll.ReplaceOne(ctx, ev.DocumentKey, ev.FullDocument, options.Replace().SetUpsert(true)); err != nil {
				log.Printf("apply replace: %v", err)
			}
		case "update":
			update := bson.D{}
			if len(ev.UpdateDescription.UpdatedFields) > 0 {
				update = append(update, bson.E{Key: "$set", Value: ev.UpdateDescription.UpdatedFields})
			}
			if len(ev.UpdateDescription.RemovedFields) > 0 {
				unset := bson.D{}
				for _, f := range ev.UpdateDescription.RemovedFields {
					unset = append(unset, bson.E{Key: f, Value: ""})
				}
				update = append(update, bson.E{Key: "$unset", Value: unset})
			}
			if len(update) > 0 {
				if _, err := coll.UpdateOne(ctx, ev.DocumentKey, update, options.UpdateOne().SetUpsert(true)); err != nil {
					log.Printf("apply update: %v", err)
				}
			}
		case "delete":
			if _, err := coll.DeleteOne(ctx, ev.DocumentKey); err != nil {
				log.Printf("apply delete: %v", err)
			}
		default:
			// ignore others
		}
	}
}

// must aborts execution when a fatal error is encountered during setup.
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// max returns the larger of two integers without importing the math package.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
