package rate

import (
	"context"
	"net/http"
)

type RateLimitedHTTPResponseWriter interface {
	http.ResponseWriter
	Close() error
}

type rlWriter struct {
	ctx context.Context
	w   http.ResponseWriter
	lim *Limiter
}

func (w *rlWriter) Header() http.Header {
	return w.w.Header()
}

func (w *rlWriter) WriteHeader(code int) {
	w.w.WriteHeader(code)
}

// Write implements http.ResponseWriter.Write with rate limiting.
//
// Rate limiting is applied BEFORE each write to prevent bursts and ensure fair
// resource distribution. The method writes data in chunks no larger than the
// burst size, acquiring tokens before each chunk write.
//
// This over-reservation trade-off is intentional:
//   - Pros: Prevents bursts, ensures predictable throughput, fair multi-tenant usage
//   - Cons: May consume tokens for bytes not actually written on partial writes
//
// This approach prioritizes preventing noisy neighbors over byte-level accuracy.
func (w *rlWriter) Write(p []byte) (int, error) {
	written := 0
	// Safe to cast: burst is validated to fit in int during limiter creation
	burstSize := int(w.lim.burst)
	for written < len(p) {
		remain := len(p) - written
		chunk := remain
		if chunk > burstSize {
			chunk = burstSize
		}

		// Acquire tokens BEFORE writing to prevent bursts
		if err := w.lim.WaitN(w.ctx, chunk); err != nil {
			return written, err
		}

		// Perform the write after rate limiting
		n, err := w.w.Write(p[written : written+chunk])
		written += n
		if err != nil {
			return written, err
		}

		// Optionally flush to reduce buffering latency for streaming
		if f, ok := w.w.(http.Flusher); ok {
			f.Flush()
		}
	}
	return written, nil
}

func (w *rlWriter) Close() error {
	w.lim.SetInUse(false)
	return nil
}
