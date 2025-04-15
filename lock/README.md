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