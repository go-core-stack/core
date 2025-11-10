# Database Abstraction Layer

This package provides a generic, database-agnostic abstraction layer with MongoDB as the primary implementation. It enables type-safe database operations with built-in support for change streams, event logging, and high availability configurations.

## Architecture Overview

The package follows a three-tier interface hierarchy:

```
StoreClient (Database Cluster)
    ↓
Store (Database)
    ↓
StoreCollection (Collection/Table)
```

This design allows for easy swapping of database implementations without affecting higher-level code.

## Core Interfaces

### StoreCollection

The `StoreCollection` interface provides collection-level operations:

```go
type StoreCollection interface {
    SetKeyType(keyType reflect.Type) error
    InsertOne(ctx context.Context, key any, data any) error
    UpdateOne(ctx context.Context, key any, data any, upsert bool) error
    FindOne(ctx context.Context, key any, data any) error
    FindMany(ctx context.Context, filter any, data any, opts ...any) error
    Count(ctx context.Context, filter any) (int64, error)
    DeleteOne(ctx context.Context, key any) error
    DeleteMany(ctx context.Context, filter any) (int64, error)
    Watch(ctx context.Context, filter any, cb WatchCallbackfn) error
    startEventLogger(ctx context.Context, eventType reflect.Type, timestamp *bson.Timestamp) error
}
```

**Key Features:**
- **Type-safe operations** through reflection-based key type registration
- **Context-aware** operations for cancellation and timeout support
- **Change monitoring** via `Watch()` with callback notifications
- **Bulk operations** with `FindMany()` and `DeleteMany()`
- **Event logging** for audit trails and debugging

### Store

The `Store` interface represents a database:

```go
type Store interface {
    GetCollection(col string) StoreCollection
    Name() string
}
```

### StoreClient

The `StoreClient` interface represents a database cluster/client:

```go
type StoreClient interface {
    GetDataStore(dbName string) Store
    GetCollection(dbName, col string) StoreCollection
    HealthCheck(ctx context.Context) error
}
```

## Files and Components

### const.go
Defines operation constants and default source identifiers:
- `MongoAddOp`: Constant for insert operations
- `MongoUpdateOp`: Constant for update/replace operations
- `MongoDeleteOp`: Constant for delete operations
- `DefaultSourceIdentifier`: Default identifier for MongoDB connections

### source.go
Thread-safe source identifier management using `sync.RWMutex`:

```go
func SetSourceIdentifier(source string)
func GetSourceIdentifier() string
```

**Important:** The source identifier can only be set once before the first `GetSourceIdentifier()` call. Attempts to change it after use will cause a panic.

**Use Case:** Allows tracking which service instance made changes to the database, useful in multi-replica deployments.

### event.go
Generic event structures for change stream monitoring and event logging:

#### Event Structure
```go
type Event[K any, E any] struct {
    Doc     DocumentKey[K]        // Document key that changed
    Op      string                 // Operation type (add/update/delete)
    Time    bson.Timestamp         // Timestamp of change
    Ns      *Namespace            // Database and collection information
    Entry   *E                    // Full document (for inserts/updates)
    Updates *UpdateDescription[E] // Update details (for updates)
}
```

#### EventLogger
```go
type EventLogger[K, E] struct {
    col StoreCollection
    ts  *bson.Timestamp
}

func NewEventLogger[K, E any](col StoreCollection) *EventLogger[K, E]
func (e *EventLogger[K, E]) StartLogger(ctx context.Context, ts *bson.Timestamp) error
```

**Features:**
- Generic event types matching your data structures
- Automatic invocation of `LogEvent()` method if implemented on entry type
- Resume capability using BSON timestamps
- Full document capture with change details

### store.go
Core interface definitions and callback types:

```go
type WatchCallbackfn func(op string, key any)
```

Defines the callback signature for change notifications through `Watch()`.

### mongo.go (517 lines)
Complete MongoDB implementation of all interfaces.

#### Key Features

**Connection Management:**
```go
type MongoConfig struct {
    Host     string  // MongoDB host (default: localhost)
    Port     string  // MongoDB port (default: 27017)
    Uri      string  // Full MongoDB URI (overrides Host/Port)
    Username string  // Authentication username
    Password string  // Authentication password
}

func NewMongoClient(config MongoConfig) (StoreClient, error)
```

**High Availability Configuration:**
- Majority write concern for replica set safety
- Journal writes enabled for durability
- SCRAM-SHA-256 authentication
- Connection pooling with retries

**Error Interpretation:**
The `interpretMongoError()` function translates MongoDB errors to library-specific error codes:
- Duplicate key errors → `errors.AlreadyExists`
- Not found errors → `errors.NotFound`
- Context cancellation → `errors.Canceled`
- Timeout errors → `errors.DeadlineExceeded`

**Change Streams:**
The `Watch()` implementation:
- Runs in a separate goroutine
- Supports pipeline filters
- Automatic cleanup on context cancellation
- Type-safe key extraction and marshaling
- Panics on unexpected errors (not context cancellation)

**Event Logging:**
The `startEventLogger()` implementation:
- Uses reflection to invoke `LogEvent()` on entry types
- Supports resume tokens for crash recovery
- Captures full documents with `updateLookup` option
- Filters events by operation type

## Usage Examples

### Basic Connection and Operations

```go
// Configure connection
config := db.MongoConfig{
    Host:     "localhost",
    Port:     "27017",
    Username: "admin",
    Password: "secret",
}

// Create client
client, err := db.NewMongoClient(config)
if err != nil {
    log.Fatal(err)
}

// Get collection
col := client.GetCollection("mydb", "users")

// Set key type (required before operations)
col.SetKeyType(reflect.TypeOf(""))

// Insert document
type User struct {
    Name  string
    Email string
}
user := User{Name: "Alice", Email: "alice@example.com"}
err = col.InsertOne(ctx, "alice", user)

// Find document
var found User
err = col.FindOne(ctx, "alice", &found)

// Update document
user.Email = "newemail@example.com"
err = col.UpdateOne(ctx, "alice", user, false)

// Delete document
err = col.DeleteOne(ctx, "alice")
```

### Using Change Streams

```go
// Watch for changes
callback := func(op string, key any) {
    log.Printf("Operation %s on key: %v", op, key)
}

// Start watching (runs in background)
err := col.Watch(ctx, nil, callback)

// With filter (only watch specific documents)
filter := bson.M{"fullDocument.status": "active"}
err := col.Watch(ctx, filter, callback)
```

### Event Logging

```go
type MyEntry struct {
    Data string
}

// Implement LogEvent method for automatic logging
func (e *MyEntry) LogEvent() {
    log.Printf("Event logged: %s", e.Data)
}

// Start event logger
eventLogger := db.NewEventLogger[string, MyEntry](col)
err := eventLogger.StartLogger(ctx, nil) // nil = start from now

// Resume from timestamp
ts := bson.Timestamp{T: 1234567890, I: 1}
err := eventLogger.StartLogger(ctx, &ts)
```

### Source Identifier for Multi-Replica Tracking

```go
// Set before creating any clients (one-time only)
db.SetSourceIdentifier("service-replica-1")

// Later, when change events occur, you can track which replica made changes
identifier := db.GetSourceIdentifier() // Returns "service-replica-1"
```

## Design Patterns

### Repository Pattern
Abstracts database operations behind interfaces, enabling:
- Database implementation swapping
- Easy mocking for tests
- Clean separation of concerns

### Generic Programming
Uses Go generics for type-safe event handling:
```go
type Event[K any, E any] struct { ... }
type EventLogger[K, E] struct { ... }
```

### Observer Pattern
Change streams with callbacks allow reactive programming:
```go
col.Watch(ctx, filter, func(op string, key any) {
    // React to changes
})
```

### Error Wrapping
Consistent error handling with context:
```go
return errors.Wrap(errors.NotFound, "document not found")
```

## Thread Safety

- **mongoClient**: Safe for concurrent use across goroutines
- **mongoStore**: Safe for concurrent use across goroutines
- **mongoCollection**: Safe for concurrent use across goroutines
- **Source Identifier**: Thread-safe with `sync.RWMutex`, but one-time write semantics

## Best Practices

1. **Always set key type** before performing operations on a collection
2. **Use contexts** for cancellation and timeout control
3. **Handle error codes** instead of comparing error messages
4. **Set source identifier early** if tracking multi-replica changes
5. **Use Watch with filters** to reduce unnecessary callback invocations
6. **Implement LogEvent()** on entry types for automatic audit logging
7. **Clean up resources** by canceling contexts when done

## Testing

See `mongo_test.go` for comprehensive unit tests covering:
- Connection establishment
- CRUD operations
- Error handling
- Change stream monitoring
- Event logging

## Performance Considerations

- **Connection Pooling**: MongoDB driver handles connection pooling automatically
- **Write Concern**: Majority write concern adds latency but ensures durability
- **Change Streams**: Run in separate goroutines to avoid blocking operations
- **Bulk Operations**: Use `FindMany()` and `DeleteMany()` for batch processing

## Future Enhancements

The interface-based design allows for future implementations:
- PostgreSQL backend
- Redis backend
- In-memory backend for testing
- Custom backends with specific requirements
