# Lock Infrastructure

While working with multi threaded programing, one heavily relies and make use
of mutex, to take a lock/ownership ensuring some sort of synchronization
construct between the different threads or go routines or concurrent tasks.
Which enables taking care of break condition between them ensuring no over
stepping between concurrent executors.

While there is nothing standard that exists providing the necessary
synchronization between different processes, running on same machine or
different. When working with kubernetes applications using microservices based
arhitecture, with horizontal scaling capabilities would be running multiple
instances of the same application, where each instance with same logic would
try to perform same kind of operations based on the events received, which
would required a mutex like construct to function across microservices for
synchronization between two different processes, possibly running on different
nodes of the kubernetes cluster

TODO(Prabhjot) insert a diagram of every process interacting with synchronizer

## Solution

As a base construct Database is one of the entity every process or microservice
instance interacts with, where Database ensures reliability, consistency as
part of its internal processes. While working with databases, it also ensures
for every entry written to it won't allow if a conflicting Key already exits in
the database, which provides the base fundamental construct needed for enabling
mutex functionality across process. Thus Database is a strong candidate that
provides the synchronizer contructs.

TODO(Prabhjot) insert a diagram of every process interacting with database as synchronizer

However, one of the basic implicit construct of mutex is being ephermal, so if
a process restarts the constructs of mutex is lost and everything will start
afresh but this becomes now tricky in case of database based entries as
whenever a process dies while holding a mutex or lock, someone needs to ensure
sanity of the system by identifying that the lock is held by a process which is
no longer active and thus clears it up allowing the continuity of operations

TODO(Prabhjot) add details of the lock owner handling details

## Waiting for a lock

`sync/` deliberately ships only a non-blocking `TryAcquire` (a duplicate-key
insert means the lock is already held, and the call returns immediately).
There is no blocking `Acquire`, and no fair/FIFO lock. The recommended way to
"wait for a lock" is to drive lock usage through the **reconciler** framework
rather than parking a goroutine on the lock:

1. Register your controller for lock-release notifications with
   `LockTable.RegisterLockRelease(name, ctrl)`. Register the *same* controller
   on your own domain table (via its reconciler `Manager`) so that normal data
   changes trigger it too.
2. In `Reconcile(key)`, **first check whether there is real work pending for
   that key**. Only if there is, call `TryAcquire`.
3. If the lock is held by another replica, just return. When the holder
   releases the lock, the release notification re-invokes `Reconcile(key)` and
   you re-evaluate whether work is still pending.

Gating acquisition on actual work is the important part: it avoids the endless
take-lock / find-nothing-to-do / release-lock churn that a naive
blocking-acquire loop would cause across N replicas, and it needs no new
primitive. Under contention only one replica wins `TryAcquire`; the rest do
nothing until the lock is released, at which point they re-check for work
before trying again.

See `sync/test/lockreconciler/example.go` for a complete, runnable example
built on the existing `reconciler` constructs.

## Accepted limitations

Database-backed locks have inherent trade-offs we accept rather than engineer
around:

- **Barging, not fair.** On release, every waiter is notified and races to
  re-acquire; acquisition order is not guaranteed. Mutual exclusion
  (correctness) is guaranteed; FIFO ordering (fairness) is not.
- **Coarse-grained and relatively slow** — a round-trip plus change-stream
  propagation is on the order of tens of milliseconds. These locks suit
  briefly-held, low-contention, leader-ish sections ("one replica reconciles
  this key at a time"), not high-frequency fine-grained locking.
- **Liveness is lease-bounded** — a lock held by a crashed process is cleaned
  up only after the owner ages out (~30s by default), not instantly.

A strict FIFO / ticket (bakery) lock is intentionally **not** provided. It
would require a new atomic `FindOneAndUpdate` (returning the post-image)
primitive in `core/db`, and strict ordering is a premium property most
workloads never need. See go-core-stack/core#126 for the full rationale; it
should be gated behind a concrete, demonstrated starvation requirement rather
than built speculatively.
