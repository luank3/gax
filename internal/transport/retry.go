package transport

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy controls transient HTTP retry behavior.
type RetryPolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
}

// ShouldRetryStatus reports whether an HTTP status code is transient.
func ShouldRetryStatus(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusRequestTimeout ||
		(status >= 500 && status <= 599)
}

// ShouldRetryErr reports whether an error is a transient transport failure.
func ShouldRetryErr(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}

// BackoffDelay computes the delay before retry attempt (0-indexed), honoring Retry-After if present.
func BackoffDelay(attempt int, base time.Duration, retryAfter string) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if retryAfter = strings.TrimSpace(retryAfter); retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	shift := min(attempt, 6)
	d := base * (1 << shift)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	// +/- 20% jitter
	jitter := time.Duration(rand.Int64N(int64(d/5)+1)) - d/10
	return max(0, d+jitter)
}
