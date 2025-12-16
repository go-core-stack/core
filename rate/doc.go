// Package rate provides rate limiting primitives with dynamic capacity rebalancing.
//
// # Overview
//
// This package implements token bucket rate limiting with the ability to dynamically
// redistribute available capacity among active limiters. This is particularly useful
// in multi-tenant systems where you want to prevent noisy neighbors while maximizing
// resource utilization.
//
// # Architecture
//
// The package consists of three main components:
//
//   - LimitManager: Manages a pool of limiters and redistributes capacity
//   - Limiter: Individual rate limiter with automatic usage reporting
//   - Rate-limited wrappers: io.Reader and http.ResponseWriter adapters
//
// # Rate Limiting Strategy
//
// This package uses PRE-OPERATION rate limiting, meaning tokens are acquired BEFORE
// performing I/O operations. This design choice prioritizes:
//
//   - Burst prevention: Operations are slowed down before they execute
//   - Fair resource distribution: Prevents noisy neighbors in multi-tenant scenarios
//   - Predictable throughput: Rate limits are enforced consistently
//
// # Token Over-Reservation Trade-off
//
// When an I/O operation returns fewer bytes than requested (partial read/write),
// the tokens for the full request are still consumed. This is intentional:
//
// Benefits:
//   - Prevents gaming the rate limiter with small requests
//   - Ensures consistent rate limiting behavior
//   - Simplifies implementation and reasoning
//
// Drawback:
//   - May consume slightly more tokens than actual bytes transferred
//
// For most use cases (bandwidth control, QoS, API throttling), this trade-off is
// acceptable and preferred over post-operation metering.
//
// # Dynamic Capacity Rebalancing
//
// The LimitManager automatically redistributes available capacity among active
// limiters:
//
//   - When a limiter becomes active, it receives a share of the total capacity
//   - When a limiter becomes idle, its capacity is redistributed to others
//   - Capacity is allocated proportionally based on configured rates
//
// This ensures that unused capacity doesn't go to waste while maintaining fairness.
//
// # Example Usage
//
//	// Create a manager with 1MB/s total capacity
//	mgr := rate.NewLimitManager(1024 * 1024)
//
//	// Create limiters for different tenants
//	tenantA, _ := mgr.NewLimiter("tenant-a", 512*1024, 64*1024) // 512KB/s, 64KB burst
//	tenantB, _ := mgr.NewLimiter("tenant-b", 256*1024, 32*1024) // 256KB/s, 32KB burst
//
//	// Wrap an io.Reader with rate limiting
//	limitedReader, _ := mgr.WrapReader(ctx, "tenant-a", reader)
//	defer limitedReader.Close()
//
//	// Wrap an HTTP response writer
//	limitedWriter, _ := mgr.WrapHTTPResponseWriter(ctx, "tenant-b", w)
//	defer limitedWriter.Close()
//
// # Preventing Noisy Neighbors
//
// The combination of pre-operation rate limiting and dynamic rebalancing makes
// this package well-suited for multi-tenant systems:
//
//   - Each tenant gets a fair share of resources
//   - Bursts are prevented before they impact other tenants
//   - Unused capacity is automatically redistributed
//   - Rate limits are enforced consistently across all tenants
package rate
