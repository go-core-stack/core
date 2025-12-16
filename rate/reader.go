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
// Note on token reservation: This method acquires tokens equal to the requested
// read size (capped at burst) before performing the read. If the underlying
// reader returns fewer bytes than requested, those tokens are still consumed.
// This design prioritizes rate limit guarantees over precision—post-read token
// acquisition would be more accurate but could allow burst violations. For most
// use cases, this over-reservation is acceptable and prevents gaming the rate
// limiter with small reads.
func (r *rlReader) Read(p []byte) (int, error) {
	// Safe to cast: burst is validated to fit in int during limiter creation
	burstSize := int(r.lim.burst)
	chunk := len(p)
	if chunk > burstSize {
		chunk = burstSize
	}
	err := r.lim.WaitN(r.ctx, chunk)
	if err != nil {
		return 0, err
	}
	return r.rc.Read(p[:chunk])
}

func (r *rlReader) Close() error {
	r.lim.SetInUse(false)
	return r.rc.Close()
}
