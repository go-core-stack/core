package rate

import (
	"context"
	"io"
)

type RateLimitedReader interface {
	io.ReadCloser
}

type rlReader struct {
	ctx context.Context
	rc  io.ReadCloser
	lim *Limiter
}

// Read implements io.Reader with rate limiting.
//
// Rate limiting is applied BEFORE performing the read to prevent bursts and ensure
// fair resource distribution (preventing noisy neighbors). This means:
//   - Tokens are acquired for the requested read size (capped at burst)
//   - The read operation is then performed
//   - If the read returns fewer bytes than requested, those tokens are still consumed
//
// This over-reservation trade-off is intentional:
//   - Pros: Prevents bursts, ensures predictable rate limiting, fair multi-tenant usage
//   - Cons: May consume tokens for bytes not actually transferred on partial reads
//
// For most use cases (bandwidth control, QoS, preventing noisy neighbors), this
// approach is preferred over post-operation metering.
func (r *rlReader) Read(p []byte) (int, error) {
	// Safe to cast: burst is validated to fit in int during limiter creation
	burstSize := int(r.lim.burst)
	chunk := len(p)
	if chunk > burstSize {
		chunk = burstSize
	}

	// Acquire tokens BEFORE reading to prevent bursts
	if err := r.lim.WaitN(r.ctx, chunk); err != nil {
		return 0, err
	}

	// Perform the read after rate limiting
	return r.rc.Read(p[:chunk])
}

func (r *rlReader) Close() error {
	r.lim.SetInUse(false)
	return r.rc.Close()
}
