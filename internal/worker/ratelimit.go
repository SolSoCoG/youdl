package worker

import (
	"io"
	"time"
)

// rateLimitedReader wraps an io.Reader and limits throughput to bytesPerSec.
type rateLimitedReader struct {
	r           io.Reader
	bytesPerSec int64
	bucket      int64
	last        time.Time
}

func newRateLimitedReader(r io.Reader, bytesPerSec int64) io.Reader {
	if bytesPerSec <= 0 {
		return r
	}
	return &rateLimitedReader{
		r:           r,
		bytesPerSec: bytesPerSec,
		bucket:      bytesPerSec,
		last:        time.Now(),
	}
}

func (r *rateLimitedReader) Read(p []byte) (int, error) {
	// Refill bucket based on elapsed time
	now := time.Now()
	elapsed := now.Sub(r.last).Seconds()
	r.last = now
	r.bucket += int64(elapsed * float64(r.bytesPerSec))
	if r.bucket > r.bytesPerSec {
		r.bucket = r.bytesPerSec
	}

	// Wait if bucket is empty
	if r.bucket <= 0 {
		wait := time.Duration(float64(time.Second) * float64(-r.bucket) / float64(r.bytesPerSec))
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		time.Sleep(wait)
		r.bucket = r.bytesPerSec / 10 // give a small burst after waiting
		r.last = time.Now()
	}

	// Limit read size to available bucket
	maxRead := int(r.bucket)
	if maxRead > len(p) {
		maxRead = len(p)
	}
	if maxRead <= 0 {
		maxRead = 1
	}

	n, err := r.r.Read(p[:maxRead])
	r.bucket -= int64(n)
	return n, err
}
