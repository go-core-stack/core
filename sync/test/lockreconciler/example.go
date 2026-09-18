// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Command lockreconciler demonstrates the recommended pattern for
// coordinating work across multiple replicas using sync.LockTable together
// with the reconciler framework, WITHOUT a blocking Acquire.
//
// The core idea (see sync/README.md -> "Waiting for a lock"):
//
//   - Every replica drives work through a reconciler.
//   - On each Reconcile(key) the replica FIRST checks whether there is any
//     real work pending for that key. Only if there is does it TryAcquire()
//     the per-key lock.
//   - If the lock is already held by another replica, it simply returns. It
//     has registered for lock-release notifications, so when the current
//     holder releases, Reconcile(key) is invoked again and the replica
//     re-evaluates whether work is still pending.
//
// Gating lock acquisition on actual work is what avoids the endless
// take-lock / find-nothing-to-do / release-lock churn that a naive blocking
// Acquire loop would cause across N replicas.
package main

import (
	"context"
	"log"
	"reflect"
	"time"

	"github.com/go-core-stack/core/db"
	"github.com/go-core-stack/core/errors"
	"github.com/go-core-stack/core/reconciler"
	"github.com/go-core-stack/core/sync"
)

// jobKey identifies a unit of work. It is used both as the domain key (in the
// jobs collection) and as the lock key (in the lock table), so a lock-release
// notification maps directly back to the job it guards.
type jobKey struct {
	ID string `bson:"id,omitempty"`
}

// jobData is the domain record for a job. Presence of the record means the job
// still needs processing; processing completes by deleting it.
type jobData struct {
	Desc string `bson:"desc,omitempty"`
}

// jobTable watches the jobs collection and drives the reconciler on domain
// changes (new / updated jobs). It is the "normal", work-driven event source.
type jobTable struct {
	reconciler.ManagerImpl
	col db.StoreCollection
}

func (t *jobTable) Callback(op string, wKey any) {
	t.NotifyCallback(wKey)
}

// ReconcilerGetAllKeys seeds reconciliation of jobs that already exist when a
// controller registers. Seeding is best-effort: on a transient DB error the
// returned list is empty and those jobs will be re-driven by the change stream.
func (t *jobTable) ReconcilerGetAllKeys() []any {
	var entries []struct {
		Key *jobKey `bson:"_id,omitempty"`
	}
	keys := []any{}
	err := t.col.FindMany(context.Background(), nil, &entries)
	if err != nil {
		log.Printf("jobTable: failed to list jobs for seeding: %s", err)
		return keys
	}
	for _, e := range entries {
		keys = append(keys, e.Key)
	}
	return keys
}

// jobProcessor is the shared controller. The SAME instance is registered on
// two event sources:
//
//   - jobTable          -> domain-driven reconciliation (new / updated work)
//   - lockTable release -> re-driven when a peer releases a lock
type jobProcessor struct {
	locks *sync.LockTable[jobKey]
	jobs  db.StoreCollection
}

func (p *jobProcessor) Reconcile(k any) (*reconciler.Result, error) {
	key, ok := k.(*jobKey)
	if !ok {
		// unexpected key type; nothing to do. Log it so a misconfigured
		// registration is easy to spot when running the example.
		log.Printf("jobProcessor: unexpected key type %T, skipping", k)
		return nil, nil
	}

	// Step 1 (the important one): gate on real work BEFORE touching the lock.
	// If there is nothing to do for this key, return immediately. This is what
	// prevents the take-lock / release-lock churn: a lock-release notification
	// only leads to an acquire if work is actually pending.
	pending, err := p.hasPendingWork(key)
	if err != nil {
		// Transient read error: returning err requeues via the pipeline.
		// NOTE: error-driven requeues have no built-in backoff, so a sustained
		// DB outage will hot-loop this key until the error clears. If that
		// matters for your workload, return
		// &reconciler.Result{RequeueAfter: <d>} instead of a raw error to space
		// out retries -- see sync/README.md -> "Variants of the pattern".
		return nil, err
	}
	if !pending {
		return nil, nil
	}

	// Step 2: try to take exclusive ownership. Non-blocking.
	lock, err := p.locks.TryAcquire(context.Background(), key)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			// A peer holds the lock. Do nothing now -- we are registered for
			// lock-release, so Reconcile(key) fires again when it is freed and
			// we re-check whether work is still pending.
			return nil, nil
		}
		// Transient DB error: requeue. Same no-backoff caveat as the
		// hasPendingWork error path above -- use RequeueAfter here too if a
		// sustained outage should not hot-loop.
		return nil, err
	}
	defer func() {
		// A failed Close does not release the lock immediately; the lock ages
		// out at the lease timeout, delaying other waiters until then. Nothing
		// to retry here, but log it so the failure is visible.
		if err := lock.Close(); err != nil {
			log.Printf("jobProcessor: failed to release lock for key %q: %s", key.ID, err)
		}
	}()

	// Step 3: we hold the lock -- do the work.
	return p.process(key)
}

// hasPendingWork reports whether the job still needs processing. Here, presence
// of the job record means work is pending.
func (p *jobProcessor) hasPendingWork(key *jobKey) (bool, error) {
	data := &jobData{}
	err := p.jobs.FindOne(context.Background(), key, data)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// process performs the actual work while holding the lock. Completion is
// represented by deleting the job record.
func (p *jobProcessor) process(key *jobKey) (*reconciler.Result, error) {
	log.Printf("processing job %q under exclusive lock", key.ID)

	// ... real work goes here ...

	err := p.jobs.DeleteOne(context.Background(), key)
	if err != nil && !errors.IsNotFound(err) {
		// could not mark complete; requeue and retry. The lock is released by
		// the deferred Close in Reconcile.
		return nil, err
	}
	return nil, nil
}

func main() {
	ctx, cancelFn := context.WithCancel(context.Background())
	defer time.Sleep(2 * time.Second)
	defer cancelFn()

	config := &db.MongoConfig{
		Host:     "localhost",
		Port:     "27017",
		Username: "root",
		Password: "password",
	}

	client, err := db.NewMongoClient(config)
	if err != nil {
		log.Panicf("failed to connect to mongo DB Error: %s", err)
	}

	if err = client.HealthCheck(context.Background()); err != nil {
		log.Panicf("failed to perform Health check with DB Error: %s", err)
	}

	s := client.GetDataStore("test-sync")

	// Owner infra is mandatory before using any sync primitive.
	err = sync.InitializeOwnerWithUpdateInterval(ctx, s, "job-worker", 10)
	if err != nil && !errors.IsAlreadyExists(err) {
		log.Panicf("failed to initialize sync owner: %s", err)
	}

	// Domain table: the jobs collection + its reconciler manager. This is the
	// normal, work-driven event source.
	jobsCol := s.GetCollection("jobs")
	if err = jobsCol.SetKeyType(reflect.TypeOf(&jobKey{})); err != nil {
		log.Panicf("failed to set job key type: %s", err)
	}
	jobs := &jobTable{col: jobsCol}
	if err = jobs.Initialize(ctx, jobs); err != nil {
		log.Panicf("failed to initialize job table manager: %s", err)
	}
	if err = jobsCol.Watch(ctx, nil, jobs.Callback); err != nil {
		log.Panicf("failed to watch jobs collection: %s", err)
	}

	// Lock table guarding per-job exclusive processing.
	locks, err := sync.LocateLockTable[jobKey](s, "job-locks")
	if err != nil {
		log.Panicf("failed to locate lock table: %s", err)
	}

	// One shared controller, two event sources.
	proc := &jobProcessor{locks: locks, jobs: jobsCol}

	// (a) domain-driven: react to new / updated jobs.
	if err = jobs.Register("job-processor", proc); err != nil {
		log.Panicf("failed to register domain reconciler: %s", err)
	}
	// (b) release-driven: re-evaluate when a peer frees a job lock.
	if err = locks.RegisterLockRelease("job-processor-onrelease", proc); err != nil {
		log.Panicf("failed to register lock-release reconciler: %s", err)
	}

	// Seed a sample job so the demo has something to process.
	err = jobsCol.InsertOne(context.Background(), &jobKey{ID: "job-1"}, &jobData{Desc: "sample work"})
	if err != nil && !errors.IsAlreadyExists(err) {
		log.Panicf("failed to insert sample job: %s", err)
	}

	for {
		// keep the process alive; the owner heartbeat and reconciler
		// pipelines run in background goroutines.
		time.Sleep(5 * time.Second)
	}
}
