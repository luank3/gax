package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Tracer observes outgoing HTTP requests, responses, and SSE events.
type Tracer interface {
	TraceRequest(method, url string, headers http.Header, body []byte)
	TraceResponse(status int, elapsed time.Duration, err error)
	TraceSSE(event, data string, elapsed time.Duration)
}

// Client wraps net/http.Client with retry, idle timeout, and optional tracing.
type Client struct {
	HTTP   *http.Client
	Retry  RetryPolicy
	Tracer Tracer
}

// NewClient constructs a provider-safe HTTP client that does not follow redirects
// and honors GAX_CA_CERT / SSL_CERT_FILE when configured.
func NewClient(retry RetryPolicy, tracer Tracer) *Client {
	hc := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if tr := buildTransport(); tr != nil {
		hc.Transport = tr
	}
	return &Client{
		HTTP:   hc,
		Retry:  retry,
		Tracer: tracer,
	}
}

func buildTransport() *http.Transport {
	caPath := os.Getenv("GAX_CA_CERT")
	if caPath == "" {
		caPath = os.Getenv("SSL_CERT_FILE")
	}
	if caPath == "" {
		return nil
	}
	pemData, err := os.ReadFile(caPath)
	if err != nil {
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return tr
}

// DoGet performs a traced HTTP GET request and returns body bytes (up to maxBytes).
func (c *Client) DoGet(ctx context.Context, url string, headers http.Header, maxBytes int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c != nil && c.Tracer != nil {
		c.Tracer.TraceRequest(http.MethodGet, url, req.Header, nil)
	}
	t0 := time.Now()
	httpClient := http.DefaultClient
	if c != nil && c.HTTP != nil {
		httpClient = c.HTTP
	}
	resp, err := httpClient.Do(req)
	elapsed := time.Since(t0)
	if err != nil {
		if c != nil && c.Tracer != nil {
			c.Tracer.TraceResponse(0, elapsed, err)
		}
		return nil, 0, err
	}
	defer resp.Body.Close()
	if c != nil && c.Tracer != nil {
		c.Tracer.TraceResponse(resp.StatusCode, elapsed, nil)
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	return raw, resp.StatusCode, err
}

// DoStream executes an HTTP request with retries on transient pre-stream failures
// and returns the open response body wrapped with idleTimeout enforcement.
func (c *Client) DoStream(ctx context.Context, method, url string, headers http.Header, body []byte, idleTimeout time.Duration, onRetry func(attempt int, delay time.Duration, reason string)) (*http.Response, error) {
	maxAttempts := c.Retry.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		var bodyReader io.Reader
		if len(body) > 0 {
			bodyReader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return nil, err
		}
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}

		if c.Tracer != nil {
			c.Tracer.TraceRequest(method, url, req.Header, body)
		}
		t0 := time.Now()
		resp, err := c.HTTP.Do(req)
		elapsed := time.Since(t0)

		if err != nil {
			if c.Tracer != nil {
				c.Tracer.TraceResponse(0, elapsed, err)
			}
			if attempt+1 < maxAttempts && ShouldRetryErr(err) {
				delay := BackoffDelay(attempt, c.Retry.BaseDelay, "")
				if onRetry != nil {
					onRetry(attempt+1, delay, err.Error())
				}
				if sleepErr := sleepCtx(ctx, delay); sleepErr != nil {
					return nil, sleepErr
				}
				continue
			}
			return nil, err
		}

		if c.Tracer != nil {
			c.Tracer.TraceResponse(resp.StatusCode, elapsed, nil)
		}
		if ShouldRetryStatus(resp.StatusCode) && attempt+1 < maxAttempts {
			retryAfter := resp.Header.Get("Retry-After")
			_ = resp.Body.Close()
			delay := BackoffDelay(attempt, c.Retry.BaseDelay, retryAfter)
			if onRetry != nil {
				onRetry(attempt+1, delay, fmt.Sprintf("HTTP %d", resp.StatusCode))
			}
			if sleepErr := sleepCtx(ctx, delay); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}

		if idleTimeout > 0 {
			resp.Body = &idleTimeoutReadCloser{
				rc:      resp.Body,
				timeout: idleTimeout,
			}
		}
		return resp, nil
	}
	return nil, fmt.Errorf("exhausted retries")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type idleTimeoutReadCloser struct {
	rc      io.ReadCloser
	timeout time.Duration
}

func (r *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	type res struct {
		n   int
		err error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := r.rc.Read(p)
		ch <- res{n, err}
	}()
	t := time.NewTimer(r.timeout)
	defer t.Stop()
	select {
	case out := <-ch:
		return out.n, out.err
	case <-t.C:
		_ = r.rc.Close()
		return 0, fmt.Errorf("stream idle timeout after %s", r.timeout)
	}
}

func (r *idleTimeoutReadCloser) Close() error {
	return r.rc.Close()
}
