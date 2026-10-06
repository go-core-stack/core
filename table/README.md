# Table Abstraction Layer

This package provides high-level, type-safe table abstractions built on top of the database layer. It offers two main implementations: a basic `Table` for direct database access and a `CachedTable` with in-memory caching for performance-critical applications.

## Architecture Overview

The table package sits on top of the `db` package and integrates with the `reconciler` package:

```
Table Layer (table/)
    ↓
Database Layer (db/)
    ↓
MongoDB
```

Both implementations integrate with the reconciler infrastructure for key enumeration and change notifications.

## Core Types

### Table[K, E]

A generic table abstraction providing type-safe operations without caching.

```go
type Table[K any, E any] struct {
    reconciler.ManagerImpl
    col db.StoreCollection
}
```

**Type Parameters:**
- `K`: Key type (must not be a pointer)
- `E`: Entry/document type (must not be a pointer)

**Features:**
- Direct database access
- Type safety through Go generics
- Reconciler integration for change notifications
- Automatic key type registration
- Watch callback support

### CachedTable[K, E]

An enhanced table with in-memory caching for fast reads.

```go
type CachedTable[K comparable, E any] struct {
    reconciler.ManagerImpl
    cacheMu sync.RWMutex
    cache   map[K]*E
    col     db.StoreCollection
}
```

**Type Parameters:**
- `K`: Key type (must be comparable for use as map key)
- `E`: Entry/document type (must not be a pointer)

**Features:**
- In-memory cache with `map[K]*E`
- Thread-safe cache access with `sync.RWMutex`
- Automatic cache synchronization via change streams
- Eager loading on initialization
- Separate methods for cached vs. direct database access

## Files and Components

### generic.go

Implements the basic `Table[K, E]` type.

#### Initialization

```go
func (mgr *Table[K, E]) Initialize(
    ctx context.Context,
    col db.StoreCollection,
    callback reconciler.CallbackFunc,
) error
```

**Steps performed:**
1. Validates that K and E are not pointer types
2. Registers the key type with the collection
3. Sets up watch callback for change notifications
4. Initializes the reconciler manager
5. Starts watching for database changes

#### CRUD Operations

**Insert:**
```go
func (mgr *Table[K, E]) Insert(ctx context.Context, key K, entry E) error
```
Inserts a new entry. Returns `errors.AlreadyExists` if key exists.

**Update:**
```go
func (mgr *Table[K, E]) Update(ctx context.Context, key K, entry E, upsert bool) error
```
Updates an existing entry. If `upsert=true`, creates if not exists.

**Locate (Upsert):**
```go
func (mgr *Table[K, E]) Locate(ctx context.Context, key K, entry E) error
```
Convenience method for upsert operations.

**Find:**
```go
func (mgr *Table[K, E]) Find(ctx context.Context, key K) (*E, error)
```
Retrieves a single entry by key. Returns pointer to entry or `errors.NotFound`.

**FindMany:**
```go
func (mgr *Table[K, E]) FindMany(
    ctx context.Context,
    filter any,
    entries *[]*E,
    opts ...any,
) error
```
Retrieves multiple entries matching a filter. Supports pagination via options.

**DeleteKey:**
```go
func (mgr *Table[K, E]) DeleteKey(ctx context.Context, key K) error
```
Deletes a single entry by key.

**DeleteByFilter:**
```go
func (mgr *Table[K, E]) DeleteByFilter(ctx context.Context, filter any) (int64, error)
```
Deletes multiple entries matching a filter. Returns count of deleted entries.

#### Reconciler Integration

```go
func (mgr *Table[K, E]) ReconcilerGetAllKeys(ctx context.Context) ([]any, error)
```
Returns all keys in the table for reconciler enumeration.

### cached_generic.go

Implements the `CachedTable[K, E]` type with in-memory caching.

#### Initialization

```go
func (mgr *CachedTable[K, E]) Initialize(
    ctx context.Context,
    col db.StoreCollection,
    callback reconciler.CallbackFunc,
) error
```

**Steps performed:**
1. Same validation and registration as `Table`
2. Initializes empty cache `map[K]*E`
3. **Eager loads all entries** from database into cache
4. Sets up watch callback for cache synchronization
5. Starts watching for database changes

**Important:** The cache is fully populated during initialization, which may take time for large tables.

#### Write Operations

**Insert:**
```go
func (mgr *CachedTable[K, E]) Insert(ctx context.Context, key K, entry E) error
```
Writes to database. Cache updated via watch callback.

**Update:**
```go
func (mgr *CachedTable[K, E]) Update(ctx context.Context, key K, entry E, upsert bool) error
```
Writes to database. Cache updated via watch callback.

**Locate:**
```go
func (mgr *CachedTable[K, E]) Locate(ctx context.Context, key K, entry E) error
```
Upsert operation. Cache updated via watch callback.

**Delete Operations:**
```go
func (mgr *CachedTable[K, E]) DeleteKey(ctx context.Context, key K) error
func (mgr *CachedTable[K, E]) DeleteByFilter(ctx context.Context, filter any) (int64, error)
```
Writes to database. Cache updated via watch callback.

#### Read Operations

**Find (Cached):**
```go
func (mgr *CachedTable[K, E]) Find(ctx context.Context, key K) (*E, error)
```
**Fast path:** Returns directly from cache without database access.
- Uses `RLock` for concurrent read safety
- Returns cached pointer or `errors.NotFound`
- **No database I/O**

**DBFind (Direct):**
```go
func (mgr *CachedTable[K, E]) DBFind(ctx context.Context, key K) (*E, error)
```
**Bypass cache:** Queries database directly.
- Use when you need guaranteed consistency
- Use when cache might be stale

**DBFindMany:**
```go
func (mgr *CachedTable[K, E]) DBFindMany(
    ctx context.Context,
    filter any,
    entries *[]*E,
    opts ...any,
) error
```
Queries database with filter. Supports pagination options.

#### Cache Management

**watchCallback (Internal):**
```go
func (mgr *CachedTable[K, E]) watchCallback(op string, key any) error
```

Handles change stream events to synchronize cache:
- **Add/Update operations**: Fetches entry from DB and updates cache
- **Delete operations**: Removes entry from cache
- Thread-safe with write lock during updates

**Cache Synchronization Flow:**
```
Database Change
    ↓
Watch Callback Triggered
    ↓
DBFind() to get latest data
    ↓
Lock cache with write lock
    ↓
Update/Delete cache entry
    ↓
Unlock cache
    ↓
Notify Reconciler
```

#### Reconciler Integration

```go
func (mgr *CachedTable[K, E]) ReconcilerGetAllKeys(ctx context.Context) ([]any, error)
```
Returns all keys from cache (not database).

## Usage Examples

### Basic Table Usage

```go
import (
    "context"
    "your-project/db"
    "your-project/table"
)

// Define your entry type
type User struct {
    Name   string
    Email  string
    Active bool
}

// Initialize database collection
client, _ := db.NewMongoClient(config)
col := client.GetCollection("mydb", "users")

// Create table
var userTable table.Table[string, User]

// Initialize with reconciler callback
callback := func(ctx context.Context, key any) error {
    log.Printf("User changed: %v", key)
    return nil
}

err := userTable.Initialize(ctx, col, callback)

// Insert user
user := User{Name: "Alice", Email: "alice@example.com", Active: true}
err = userTable.Insert(ctx, "alice", user)

// Find user
foundUser, err := userTable.Find(ctx, "alice")
if err != nil {
    log.Fatal(err)
}

// Update user
foundUser.Email = "newemail@example.com"
err = userTable.Update(ctx, "alice", *foundUser, false)

// Delete user
err = userTable.DeleteKey(ctx, "alice")

// Find many with filter
var users []*User
filter := bson.M{"active": true}
err = userTable.FindMany(ctx, filter, &users)
```

### Cached Table Usage

```go
// Create cached table
var userCache table.CachedTable[string, User]

// Initialize (loads all entries into cache)
err := userCache.Initialize(ctx, col, callback)

// Fast cached reads (no database I/O)
user, err := userCache.Find(ctx, "alice") // Returns from cache

// Direct database access when needed
user, err := userCache.DBFind(ctx, "alice") // Bypasses cache

// Writes update database and cache automatically
newUser := User{Name: "Bob", Email: "bob@example.com", Active: true}
err = userCache.Insert(ctx, "bob", newUser)
// Cache automatically updated via watch callback

// Query database directly
var activeUsers []*User
filter := bson.M{"active": true}
err = userCache.DBFindMany(ctx, filter, &activeUsers)
```

### Working with Filters and Pagination

```go
// Complex filter
filter := bson.M{
    "active": true,
    "email": bson.M{"$regex": "@example.com$"},
}

// Pagination options
opts := options.Find().
    SetLimit(10).
    SetSkip(20).
    SetSort(bson.M{"name": 1})

var users []*User
err := userTable.FindMany(ctx, filter, &users, opts)
```

### Bulk Delete

```go
// Delete all inactive users
filter := bson.M{"active": false}
count, err := userTable.DeleteByFilter(ctx, filter)
log.Printf("Deleted %d inactive users", count)
```

### Conditions

`Cond` values are typed conditions on a table's entry type. The same values select rows and guard a conditional write, so a find of candidates and the claim on each test the same conditions. On a scoped `CachedTable` the claim also requires the table's configured filter, which a find does not apply; a candidate outside the scope is simply not claimed.

```go
claimable := []table.Cond{
    table.In("status", "queued", "requeued"),   // equals one of
    table.LessEq("notBefore", now),             // ranges: Less, LessEq, Greater, GreaterEq
}
jobs, err := jobsTable.FindManyWithOpts(ctx, nil, table.Where(claimable...), table.WithLimit(20))
n, err := jobsTable.CountWhere(ctx, claimable...)
deleted, err := jobsTable.DeleteWhere(ctx, table.In("status", "done"))
```

| Condition | Meaning |
|---|---|
| `Match(&F{...})` | every **set** field equals the stored value: a non-nil pointer, or a non-pointer that is not its zero value (an empty non-nil slice counts as set). Nested structs compare as dotted paths. `F` is the entry type or a separate filter struct (below). |
| `MatchZero(paths...)` | the field is its zero value, `null` or absent (`omitempty` stores a zero as absent) |
| `In` / `NotIn` | the field equals one / none of the values. A zero value among them also matches (`In`) or excludes (`NotIn`) `null` and absent; otherwise `NotIn` matches rows where the field is absent. |
| `Less`, `LessEq`, `Greater`, `GreaterEq` | ranges on ordered fields: numbers, strings, dates, object ids, timestamps, decimals. **An absent or null field matches no range**, and with `omitempty` a zero is stored as absent: make a ranged field a pointer with `omitempty`, set on every row at insert. |
| `Raw(filter)` | a filter document, unchecked: the escape hatch for `$or`, `$expr`, regular expressions |

Rules:

- Paths are bson paths of the entry type (`"spec.owner"`), checked against its fields when the operation runs; values are converted to the field's type (`1` works for an `int64`, `"queued"` for a named string type). A misspelt path or a value that does not fit is `InvalidArgument` at run time, not a filter that silently matches nothing. (Only `Match` is typed by the entry at compile time; a `Cond` is not generic, which keeps `Where` and `If` short.)
- A `Cond` does not change after it is built: values are copied (the value behind a pointer included), `Match` values and `Raw` documents are encoded then, so changing or clearing the caller's struct, slice or map afterwards cannot reach it. The same conditions can be built once and used for a find and the update that follows it.
- **Separate filter structs.** `Match` and `WithIncrement` take the entry type or any struct whose bson paths name fields of it. Paths come from the filter struct's own tags, read as the driver reads them (the tag's first part, or the lower-cased field name when it is empty; `-` skips; `inline` flattens), and a dotted tag names a nested field:

  ```go
  type Running struct {
      Status string `bson:"status"`
      Owner  string `bson:"spec.owner"`
  }
  ok, err := jobs.UpdateWithOpts(ctx, key, &claim, table.If(table.Match(&Running{Status: "running", Owner: me})))
  ```

  Every path must exist on the entry, and every value must encode to a type the entry field holds; the numeric types match each other, as MongoDB compares them by value. An increment is converted to the field's own type: a fraction into an integer field is refused, and a `bson.Decimal128` field takes only `bson.Decimal128` increments. A field or value that contains a map anywhere cannot be compared, including one held behind an interface (`any`, `[]any`, a `bson.D` element). A value held behind an interface that encodes itself, or nests too deep to follow (64 steps, each pointer, interface, element and field counting one), is refused as unchecked.
- Types the driver encodes itself (`time.Time`, `bson.ObjectID`, `bson.Decimal128`, `bson.Timestamp`, ...) are compared as values, never walked as structs. A field whose type has its own encoder (`MarshalBSON`/`MarshalBSONValue`, on the type or its pointer) — or holds one, in a list or nested struct — stores whatever that encoder writes (an inlined struct is the exception: the driver writes its fields into the outer document and ignores its encoder, so its fields are conditions like any other), so typed conditions and `WithIncrement` refuse it and point to `Raw`; so are values of such types given to `In`, `NotIn`, a range or `WithIncrement`, or set in a field of a `Match` filter struct (the filter struct itself may have an encoder: only its Go fields are read). The same holds for the entry type itself: if it (or its pointer type) has its own encoder, its Go fields do not describe what is stored, so every typed condition, `WithIncrement` and `WithUnset` is refused and points to `Raw`; plain entry writes and `Raw` still work. `Raw` and the `filter` argument are encoded once, exactly as given, and those bytes are what is sent: an encoder declared on a pointer type runs when you pass a pointer, as for a stored row, and not for a plain value — as the driver itself does. A document that encodes to nothing is refused by `Raw` and dropped as a `filter`.
- Field resolution follows the encoder: when an inlined field shares its name with an outer one, the outer field and its subtree win, and the hidden field cannot be named.
- Conditions are ANDed with each other and with the `filter` argument. A path may appear in one equality-type condition (`Match`, `MatchZero`, `In`, `NotIn`) and not also in a range; ranges on one path combine into a window.
- On a `CachedTable`, `CountWhere`, `DeleteWhere` and `UpdateWithOpts` with `If` AND in the table's configured filter, so a table scoped to part of a shared collection counts, deletes and updates only its own rows. Finds that query the database do not apply the configured filter, with or without `Where`: a `Where` narrows a find only by its own conditions. Add `Raw(scope)` to apply the scope to a find. `Count` and `DeleteByFilter` are unchanged.
- `Where()` and `DeleteWhere()` with no conditions are refused.
- `DeleteWhere` and `DeleteByFilter` differ on purpose:

  | | `DeleteWhere(ctx, conds...)` | `DeleteByFilter(ctx, filter)` |
  |---|---|---|
  | nothing matches | `(0, nil)` | `NotFound` |
  | no conditions / empty filter | refused | deletes every row (`bson.D{}`) |
  | scoped `CachedTable` | only rows in scope | the whole collection |

  A delete of several rows is not atomic: on an error the count is 0 and some rows may already be gone.
- Code that applies `FindOptions` itself (wrappers, test fakes) reads the conditions with `FindOptions.Conds()` and their filter with `BuildFilter[E](conds...)`. `Match` values in that filter are already encoded (`bson.RawValue`), so compare filters by their encoding (`bson.Marshal` both sides), not with `reflect.DeepEqual`.

### Conditional updates

`UpdateWithOpts` writes `entry` as `Update` does, together with the changes and conditions in its options, in one atomic write to one row. It never inserts.

```go
// Claim a job: of several replicas, exactly one gets true. (Replicas that
// all find the same top-N candidates and race for them waste attempts under
// load; correctness holds, since each row has one winner.)
won, err := jobs.UpdateWithOpts(ctx, key,
    &Job{Status: p("running"), Worker: p(me), Claim: p(token)},
    table.If(table.In("status", "queued", "requeued")),
    table.WithIncrement(&Job{Attempts: 1}))

// Finish only under my claim: a stale owner gets false.
done, err := jobs.UpdateWithOpts(ctx, key, &Job{Status: p("done")},
    table.If(table.Match(&Job{Status: p("running"), Claim: p(token)})))

// Release n, never below zero.
ok, err := quotas.UpdateWithOpts(ctx, key, nil,
    table.If(table.GreaterEq("used", n)),
    table.WithIncrement(&Quota{Used: -n}))
```

- `true`: applied (a row matched but left unchanged counts). `false`: no row with this key held the conditions; it may not exist. Without `If`, a missing row is `NotFound`, as for `Update`.
- `entry` is encoded as `Update` encodes it, which is not the "set field" rule conditions use: a field without `omitempty` is written even when zero, so a sparse entry needs `omitempty` on every field and pointers for nested structs. It may be nil when the call only increments (`WithIncrement`, negative to decrement) or unsets (`WithUnset`).
- Refused with `InvalidArgument`: `If()` with no conditions, an increment of a non-numeric field, an unknown unset path, the same path (or a path and its parent) written twice, and nothing to write. Server write errors meaning the request does not fit the stored data (`$inc` on a stored `null`) are `InvalidArgument`; a write-concern error is `Unavailable`.
- **Unknown outcomes.** After `Unavailable`, a cancelled context or a client shutdown, the write may or may not have been applied, and a retry can get `false` for its own earlier write: `false` means "the row does not hold the conditions now", not "someone else won". Write a marker only you could have written (a claim token, made once per claim and reused across its retries) and read the row back to look for it. Unconditional increments are not idempotent across retries.
- **Read-modify-write of a whole entry** needs a version field that every writer goes through: read the row (`Find` on a `Table`, `DBFind` on a `CachedTable`), copy it, change it, then write with `If(Match(&E{Version: cur.Version}))` and `WithIncrement(&E{Version: 1})`, the version tagged `omitempty` and zeroed in the copy. A changed field must be non-zero or cleared with `WithUnset`: with `omitempty`, a zero is left out of the write. Rows start at version 1; a row with no version is brought in once with `If(MatchZero("version"))` and the version set in the entry. A row deleted and re-inserted under the same key starts its version again.

## Design Patterns

### Generic Programming
Type safety through Go generics:
```go
Table[K any, E any]           // Flexible types
CachedTable[K comparable, E any] // K must support equality
```

### Decorator Pattern
`CachedTable` extends `Table` behavior with caching.

### Observer Pattern
Change streams propagate updates:
```
DB Change → Watch Callback → Cache Update → Reconciler Notify
```

### Write-Through Cache
- Writes go directly to database
- Cache updated asynchronously via watch callbacks
- Eventual consistency model

### Read-Write Lock
Optimizes concurrent cache access:
- Multiple concurrent readers with `RLock()`
- Exclusive writer with `Lock()`

## Thread Safety

### Table[K, E]
- Safe for concurrent use (backed by thread-safe database operations)

### CachedTable[K, E]
- **Cache reads:** Multiple concurrent readers via `RLock()`
- **Cache writes:** Exclusive access via `Lock()`
- **Database operations:** Thread-safe through db layer
- **No deadlocks:** Lock held for minimal duration

## Performance Characteristics

### Table[K, E]
- **Reads:** Direct database access (network I/O)
- **Writes:** Direct database access
- **Best for:** Write-heavy workloads, guaranteed consistency

### CachedTable[K, E]
- **Initialization:** O(n) - loads all entries
- **Cached reads:** O(1) - in-memory map lookup, no I/O
- **Writes:** Database I/O + eventual cache update
- **Memory:** O(n) - stores all entries in RAM
- **Best for:** Read-heavy workloads, large tables with frequent access

## Consistency Model

### Table[K, E]
- **Strong consistency:** Always reads from database
- **Immediate visibility:** Writes immediately visible to all readers

### CachedTable[K, E]
- **Eventual consistency:** Cache updated asynchronously
- **Typical lag:** Milliseconds (depends on change stream latency)
- **Consistency guarantees:**
  - Writes always go to database first
  - Cache never has data that wasn't written
  - Cache may be slightly stale (bounded staleness)

## When to Use Which Implementation

### Use Table[K, E] when:
- Strong consistency is required
- Write-heavy workload
- Memory constraints (large tables)
- Fresh data more important than read speed

### Use CachedTable[K, E] when:
- Read-heavy workload (>90% reads)
- Acceptable eventual consistency
- Low read latency critical
- Table size fits comfortably in memory
- Frequent access to same keys

## Best Practices

1. **Type choices:**
   - Don't use pointer types for K or E
   - K must be comparable for CachedTable
   - Use simple types for keys (string, int, UUID)

2. **Initialization:**
   - Initialize during startup, not per-request
   - Handle initialization errors (database connectivity)
   - Be aware CachedTable loads all data upfront

3. **Error handling:**
   - Check for `errors.AlreadyExists` on Insert
   - Check for `errors.NotFound` on Find
   - Handle context cancellation gracefully

4. **Cache usage:**
   - Use `Find()` for cached reads (fast path)
   - Use `DBFind()` when consistency matters
   - Consider cache size vs. memory available

5. **Filters:**
   - Use MongoDB BSON filters
   - Leverage indexes for better performance
   - Use pagination for large result sets

6. **Reconciler integration:**
   - Implement meaningful callback logic
   - Keep callbacks fast (avoid blocking)
   - Handle callback errors appropriately

## Testing

See `cached_generic_test.go` for comprehensive unit tests covering:
- Initialization and eager loading
- Cache synchronization via watch callbacks
- Concurrent read/write operations
- Thread safety verification

## Future Enhancements

- **Cache eviction policies** for memory-constrained environments
- **Partial caching** with LRU eviction
- **Read-through cache** fallback to database on cache miss
- **Cache statistics** for monitoring hit rates
- **Batch operations** for improved performance
