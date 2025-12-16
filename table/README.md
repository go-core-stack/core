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
