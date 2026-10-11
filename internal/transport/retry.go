package transport

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const maxRetryDelay = 30 * time.Second

var retryClockStart = time.Now()

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	IdleTimeout time.Duration
}

func DefaultRetryPolicy(additionalRetries int, baseDelay, idleTimeout time.Duration) RetryPolicy {
	if additionalRetries < 0 {
		additionalRetries = 0
	}
	if baseDelay < 0 {
		baseDelay = 0
	}
	if idleTimeout > 0 {
		idleTimeout = (idleTimeout + time.Second - 1) / time.Second * time.Second
	}
	return RetryPolicy{
		MaxAttempts: additionalRetries + 1,
		BaseDelay:   baseDelay,
		MaxDelay:    maxRetryDelay,
		IdleTimeout: idleTimeout,
	}
}

func ShouldRetry(status int, body string) bool {
	if status == 0 {
		return true
	}
	if status >= 200 && status < 300 {
		return false
	}
	if status == 408 {
		return true
	}
	if status == 429 {
		return !hasTerminal429Error(body)
	}
	return status >= 500 && status <= 599
}

func Delay(policy RetryPolicy, attempt int) time.Duration {
	if policy.BaseDelay <= 0 || policy.MaxDelay <= 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}
	delay := policy.BaseDelay
	if delay > policy.MaxDelay {
		delay = policy.MaxDelay
	}
	for i := 0; i < attempt; i++ {
		if delay > policy.MaxDelay/2 {
			delay = policy.MaxDelay
			break
		}
		delay *= 2
	}
	jitterPercent := 75 + time.Since(retryClockStart).Milliseconds()%51
	if delay > time.Duration(int64(^uint64(0)>>1)/jitterPercent) {
		delay = policy.MaxDelay
	} else {
		delay = delay * time.Duration(jitterPercent) / 100
	}
	if delay > policy.MaxDelay {
		delay = policy.MaxDelay
	}
	return delay
}

func SleepWithTick(ctx context.Context, delay time.Duration, tick func() bool) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if delay <= 0 {
		return tick != nil && tick()
	}
	deadline := time.Now().Add(delay)
	for {
		if tick != nil && tick() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		if remaining > 100*time.Millisecond {
			remaining = 100 * time.Millisecond
		}
		timer := time.NewTimer(remaining)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return true
		}
	}
}

func hasTerminal429Error(body string) bool {
	if body == "" {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &root) != nil {
		return false
	}
	var errorObject map[string]json.RawMessage
	if json.Unmarshal(root["error"], &errorObject) != nil || errorObject == nil {
		return false
	}
	for _, field := range []string{"type", "code"} {
		var code string
		if json.Unmarshal(errorObject[field], &code) != nil {
			continue
		}
		switch strings.ToLower(code) {
		case "usage_limit_reached", "usage_not_included", "insufficient_quota", "quota_exceeded", "gousagelimiterror":
			return true
		}
	}
	return false
}
