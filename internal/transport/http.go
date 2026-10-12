package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/luank3/gax/internal/version"
	"github.com/luank3/gax/internal/wiretrace"
)

const (
	maxErrorBodyBytes      = 4096
	maxRetryAfter          = 2 * time.Minute
	connectTimeout         = 10 * time.Second
	bufferedConnectTimeout = 2 * time.Second
	pollInterval           = 100 * time.Millisecond
)

var (
	ErrCancelled        = errors.New("request cancelled")
	ErrResponseTooLarge = errors.New("HTTP response exceeds configured size limit")
)

type Response struct {
	StatusCode int
	ErrorBody  string
	RetryAfter time.Duration
	Cancelled  bool
}

type Client struct {
	HTTP           *http.Client
	UserAgent      string
	stream         *http.Client
	bufferedClient *http.Client
	trace          *wiretrace.Logger
	caStoreMissing bool
}

func NewClient() *Client {
	rootCAs := configuredRootCAs()
	proxyRootCAs := rootCAs
	if rootCAs != nil {
		proxyRootCAs = nil
		if os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "" {
			proxyRootCAs = systemProxyRootCAs()
		}
	}
	return &Client{
		UserAgent:      "gax/" + version.Value,
		stream:         newHTTPClient(connectTimeout, rootCAs, proxyRootCAs),
		bufferedClient: newHTTPClient(bufferedConnectTimeout, rootCAs, proxyRootCAs),
		trace:          wiretrace.Default(),
		caStoreMissing: rootCAs == nil && systemRootStoreMissing(),
	}
}

func newHTTPClient(dialTimeout time.Duration, rootCAs, proxyRootCAs *x509.CertPool) *http.Client {
	transport := newTransport(dialTimeout, rootCAs, proxyRootCAs)
	var roundTripper http.RoundTripper = transport
	if rootCAs != nil {
		roundTripper = proxyCAAwareRoundTripper{transport: transport}
	}
	return &http.Client{
		Transport:     roundTripper,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func newTransport(dialTimeout time.Duration, rootCAs, proxyRootCAs *x509.CertPool) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = dialTimeout
	if rootCAs != nil {
		tlsConfig := &tls.Config{}
		if transport.TLSClientConfig != nil {
			tlsConfig = transport.TLSClientConfig.Clone()
		}
		tlsConfig.RootCAs = rootCAs
		transport.TLSClientConfig = tlsConfig
		transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			roots := rootCAs
			if proxyAddress, ok := ctx.Value(proxyTLSAddressKey{}).(string); ok && address == proxyAddress {
				roots = proxyRootCAs
			}
			serverName, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			raw, err := transport.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			tlsConfig := transport.TLSClientConfig.Clone()
			tlsConfig.RootCAs = roots
			if tlsConfig.ServerName == "" {
				tlsConfig.ServerName = serverName
			}
			tlsContext := ctx
			cancel := func() {}
			if dialTimeout > 0 {
				tlsContext, cancel = context.WithTimeout(ctx, dialTimeout)
			}
			defer cancel()
			tlsConn := tls.Client(raw, tlsConfig)
			if err := tlsConn.HandshakeContext(tlsContext); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return tlsConn, nil
		}
	}
	return transport
}

type proxyTLSAddressKey struct{}

type proxyCAAwareRoundTripper struct {
	transport *http.Transport
}

func (roundTripper proxyCAAwareRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	proxy := roundTripper.transport.Proxy
	if proxy == nil {
		proxy = http.ProxyFromEnvironment
	}
	proxyURL, err := proxy(request)
	if err != nil {
		return nil, err
	}
	if proxyURL != nil && strings.EqualFold(proxyURL.Scheme, "https") {
		address := proxyURL.Port()
		if address == "" {
			address = "443"
		}
		request = request.WithContext(context.WithValue(request.Context(), proxyTLSAddressKey{},
			net.JoinHostPort(proxyURL.Hostname(), address)))
	}
	return roundTripper.transport.RoundTrip(request)
}

// configuredRootCAs resolves the CA environment overrides used by the ported transport. A nil
// pool leaves Go's native system trust in place; a non-nil empty pool preserves an invalid or
// unreadable explicit override as a trust failure instead of silently falling back to other roots.
func configuredRootCAs() *x509.CertPool {
	if bundle := os.Getenv("CURL_CA_BUNDLE"); bundle != "" {
		return rootPoolFromFile(bundle)
	}
	certFile := os.Getenv("SSL_CERT_FILE")
	certDirs := filepath.SplitList(os.Getenv("SSL_CERT_DIR"))
	hasCertDirs := false
	for _, directory := range certDirs {
		hasCertDirs = hasCertDirs || directory != ""
	}
	if certFile == "" && !hasCertDirs {
		return nil
	}
	roots := x509.NewCertPool()
	if certFile != "" {
		appendCertFile(roots, certFile)
	}
	for _, directory := range certDirs {
		if directory == "" {
			continue
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			appendCertFile(roots, filepath.Join(directory, entry.Name()))
		}
	}
	return roots
}

func rootPoolFromFile(path string) *x509.CertPool {
	roots := x509.NewCertPool()
	appendCertFile(roots, path)
	return roots
}

func appendCertFile(roots *x509.CertPool, path string) {
	data, err := os.ReadFile(path)
	if err == nil {
		roots.AppendCertsFromPEM(data)
	}
}

var DefaultClient = NewClient()

const missingCAStoreHint = "no CA certificate store found on this system; " +
	"install the ca-certificates package or set SSL_CERT_FILE"

func systemRootStoreMissing() bool {
	roots, err := x509.SystemCertPool()
	return err != nil || roots == nil || len(roots.Subjects()) == 0
}

func (client *Client) withCAStoreHint(err error) error {
	if err == nil || client == nil || !client.caStoreMissing {
		return err
	}
	var verificationError *tls.CertificateVerificationError
	var systemRootsError x509.SystemRootsError
	if !errors.As(err, &verificationError) && !errors.As(err, &systemRootsError) {
		return err
	}
	return fmt.Errorf("%w (%s)", err, missingCAStoreHint)
}

func SSEPost(ctx context.Context, url string, headers []string, body []byte, idleTimeout time.Duration,
	callback func(SSEEvent) bool, tick func() bool) (Response, error) {
	return DefaultClient.SSEPost(ctx, url, headers, body, idleTimeout, callback, tick)
}

func (client *Client) SSEPost(ctx context.Context, url string, headers []string, body []byte,
	idleTimeout time.Duration, callback func(SSEEvent) bool, tick func() bool) (Response, error) {
	request, err := client.newRequest(ctx, http.MethodPost, url, headers, "", body)
	if err != nil {
		return Response{}, err
	}
	requestContext, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(requestContext)
	response := Response{}
	remote, cancelled, err := client.perform(request, tick, idleTimeout, cancel, false)
	if err != nil {
		response.Cancelled = cancelled
		if !cancelled {
			err = client.withCAStoreHint(err)
			response.ErrorBody = "net/http: " + err.Error()
		}
		client.traceLogger().ResponseStatus(0, response.ErrorBody)
		return response, err
	}
	defer remote.Body.Close()
	response.StatusCode = remote.StatusCode
	response.RetryAfter = parseRetryAfter(remote.Header.Get("Retry-After"), time.Now())
	trace := client.traceLogger()
	eventCallback := callback
	if trace.Enabled() {
		eventCallback = func(event SSEEvent) bool {
			trace.SSEEvent(event.Name, event.Data)
			return callback == nil || callback(event)
		}
	}
	parser := NewSSEParser(eventCallback)
	data, readErr, wasCancelled := readStream(requestContext, remote.Body, tick, idleTimeout,
		cancel, func(chunk []byte) {
			if remote.StatusCode == 0 || remote.StatusCode >= http.StatusOK &&
				remote.StatusCode < http.StatusMultipleChoices {
				parser.Feed(chunk)
			}
		})
	if !wasCancelled {
		parser.Finalize()
	}
	response.Cancelled = wasCancelled
	if readErr != nil {
		response.ErrorBody = "net/http: " + readErr.Error()
		trace.ResponseStatus(response.StatusCode, response.ErrorBody)
		return response, readErr
	}
	if wasCancelled {
		trace.ResponseStatus(response.StatusCode, "")
		return response, ErrCancelled
	}
	if remote.StatusCode < http.StatusOK || remote.StatusCode >= http.StatusMultipleChoices {
		response.ErrorBody = string(data)
		if response.ErrorBody == "" {
			response.ErrorBody = "(no response body)"
		}
	}
	trace.ResponseStatus(response.StatusCode, response.ErrorBody)
	return response, nil
}

func Get(ctx context.Context, url string, headers []string, timeout time.Duration, maxBytes int64,
	tick func() bool) ([]byte, int, error) {
	return DefaultClient.Get(ctx, url, headers, timeout, maxBytes, tick)
}

func (client *Client) Get(ctx context.Context, url string, headers []string, timeout time.Duration,
	maxBytes int64, tick func() bool) ([]byte, int, error) {
	request, err := client.newRequest(ctx, http.MethodGet, url, headers, "", nil)
	if err != nil {
		return nil, 0, err
	}
	return client.buffered(request, timeout, maxBytes, tick, false)
}

func PostJSON(ctx context.Context, url string, headers []string, body []byte, timeout time.Duration,
	maxBytes int64, tick func() bool) ([]byte, error) {
	return DefaultClient.PostJSON(ctx, url, headers, body, timeout, maxBytes, tick)
}

func (client *Client) PostJSON(ctx context.Context, url string, headers []string, body []byte,
	timeout time.Duration, maxBytes int64, tick func() bool) ([]byte, error) {
	request, err := client.newRequest(ctx, http.MethodPost, url, headers, "application/json", body)
	if err != nil {
		return nil, err
	}
	response, status, err := client.buffered(request, timeout, maxBytes, tick, false)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices || len(response) == 0 {
		return nil, fmt.Errorf("HTTP request returned status %d or an empty body", status)
	}
	return response, nil
}

func Post(ctx context.Context, url string, headers []string, contentType string, body []byte,
	timeout time.Duration, maxBytes int64, tick func() bool) ([]byte, int, error) {
	return DefaultClient.Post(ctx, url, headers, contentType, body, timeout, maxBytes, tick)
}

func (client *Client) Post(ctx context.Context, url string, headers []string, contentType string,
	body []byte, timeout time.Duration, maxBytes int64, tick func() bool) ([]byte, int, error) {
	request, err := client.newRequest(ctx, http.MethodPost, url, headers, contentType, body)
	if err != nil {
		return nil, 0, err
	}
	return client.buffered(request, timeout, maxBytes, tick, true)
}

func (client *Client) newRequest(ctx context.Context, method, url string, headers []string,
	contentType string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if agent := client.userAgent(); agent != "" {
		request.Header.Set("User-Agent", agent)
	}
	for _, line := range headers {
		name, value, found := strings.Cut(line, ":")
		if !found {
			return nil, fmt.Errorf("invalid HTTP header %q", line)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimLeft(value, " \t")
		if strings.EqualFold(name, "User-Agent") {
			request.Header.Set(name, value)
		} else {
			request.Header.Add(name, value)
		}
	}
	if contentType != "" {
		request.Header.Add("Content-Type", contentType)
	}
	traceHeaders := append([]string(nil), headers...)
	if contentType != "" && method != http.MethodGet {
		traceHeaders = append(traceHeaders, "Content-Type: "+contentType)
	}
	traceRequest := requestTrace{method: method, url: url, headers: traceHeaders, body: append([]byte(nil), body...)}
	request = request.WithContext(context.WithValue(request.Context(), traceRequestContextKey{}, traceRequest))
	return request, nil
}

type traceRequestContextKey struct{}

type requestTrace struct {
	method  string
	url     string
	headers []string
	body    []byte
}

type requestResult struct {
	response *http.Response
	err      error
}

func (client *Client) perform(request *http.Request, tick func() bool, idleTimeout time.Duration,
	cancel context.CancelFunc, buffered bool) (*http.Response, bool, error) {
	if tick != nil && tick() {
		cancel()
		return nil, true, ErrCancelled
	}
	if tick == nil && idleTimeout <= 0 {
		response, err := client.do(request, buffered)
		return response, false, err
	}
	result := make(chan requestResult, 1)
	go func() {
		response, err := client.do(request, buffered)
		result <- requestResult{response: response, err: err}
	}()
	var ticker *time.Ticker
	var tickChannel <-chan time.Time
	if tick != nil {
		ticker = time.NewTicker(pollInterval)
		tickChannel = ticker.C
		defer ticker.Stop()
	}
	var timer *time.Timer
	var idleChannel <-chan time.Time
	if idleTimeout > 0 {
		timer = time.NewTimer(idleTimeout)
		idleChannel = timer.C
		defer timer.Stop()
	}
	for {
		select {
		case outcome := <-result:
			return outcome.response, false, outcome.err
		case <-tickChannel:
			if tick() {
				cancel()
				return nil, true, ErrCancelled
			}
		case <-idleChannel:
			cancel()
			return nil, false, fmt.Errorf("no response data for %s", idleTimeout)
		case <-request.Context().Done():
			cancelled := errors.Is(request.Context().Err(), context.Canceled)
			return nil, cancelled, request.Context().Err()
		}
	}
}

func (client *Client) do(request *http.Request, buffered bool) (*http.Response, error) {
	if trace, ok := request.Context().Value(traceRequestContextKey{}).(requestTrace); ok {
		client.traceLogger().Request(trace.method, trace.url, trace.headers, trace.body)
	}
	return client.httpClient(buffered).Do(request)
}

func (client *Client) buffered(request *http.Request, timeout time.Duration, maxBytes int64,
	tick func() bool, anyStatus bool) ([]byte, int, error) {
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	if timeout > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, timeout)
		defer timeoutCancel()
	}
	request = request.WithContext(ctx)
	remote, cancelled, err := client.perform(request, tick, 0, cancel, true)
	if err != nil {
		message := ""
		if !cancelled {
			err = client.withCAStoreHint(err)
			message = "net/http: " + err.Error()
		}
		client.traceLogger().ResponseStatus(0, message)
		if cancelled {
			return nil, 0, ErrCancelled
		}
		return nil, 0, err
	}
	defer remote.Body.Close()
	body, wasCancelled, err := readBuffered(ctx, remote.Body, tick, maxBytes, cancel)
	if err != nil {
		client.traceLogger().ResponseStatus(remote.StatusCode, "net/http: "+err.Error())
		if wasCancelled {
			return nil, 0, ErrCancelled
		}
		if anyStatus {
			return nil, 0, err
		}
		return nil, remote.StatusCode, err
	}
	status := remote.StatusCode
	if !anyStatus && (status < http.StatusOK || status >= http.StatusMultipleChoices || len(body) == 0) {
		traceBody := ""
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			traceBody = string(body)
		}
		client.traceLogger().ResponseStatus(status, traceBody)
		return nil, status, fmt.Errorf("HTTP request returned status %d or an empty body", status)
	}
	traceBody := ""
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		traceBody = string(body)
	}
	client.traceLogger().ResponseStatus(status, traceBody)
	return body, status, nil
}

func readBuffered(ctx context.Context, body io.Reader, tick func() bool, maxBytes int64,
	cancel context.CancelFunc) ([]byte, bool, error) {
	results := make(chan bodyRead, 1)
	go readBodyChunks(ctx, body, results)
	var ticker *time.Ticker
	var tickChannel <-chan time.Time
	if tick != nil {
		if tick() {
			cancel()
			return nil, true, ErrCancelled
		}
		ticker = time.NewTicker(pollInterval)
		tickChannel = ticker.C
		defer ticker.Stop()
	}
	var output []byte
	for {
		select {
		case chunk := <-results:
			if len(chunk.data) > 0 {
				if tick != nil && tick() {
					cancel()
					return nil, true, ErrCancelled
				}
				if maxBytes > 0 && int64(len(output))+int64(len(chunk.data)) > maxBytes {
					return nil, false, ErrResponseTooLarge
				}
				output = append(output, chunk.data...)
			}
			if chunk.err != nil {
				if errors.Is(chunk.err, io.EOF) {
					return output, false, nil
				}
				return nil, false, chunk.err
			}
		case <-tickChannel:
			if tick() {
				cancel()
				return nil, true, ErrCancelled
			}
		case <-ctx.Done():
			cancel()
			return nil, errors.Is(ctx.Err(), context.Canceled), ctx.Err()
		}
	}
}

type bodyRead struct {
	data []byte
	err  error
}

func readBodyChunks(ctx context.Context, body io.Reader, output chan<- bodyRead) {
	buffer := make([]byte, 32*1024)
	for {
		n, err := body.Read(buffer)
		if n > 0 || err != nil {
			chunk := bodyRead{data: append([]byte(nil), buffer[:n]...), err: err}
			select {
			case output <- chunk:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func readStream(ctx context.Context, body io.Reader, tick func() bool, idleTimeout time.Duration,
	cancel context.CancelFunc, consume func([]byte)) ([]byte, error, bool) {
	results := make(chan bodyRead, 1)
	go readBodyChunks(ctx, body, results)
	var ticker *time.Ticker
	var tickChannel <-chan time.Time
	if tick != nil {
		if tick() {
			cancel()
			return nil, nil, true
		}
		ticker = time.NewTicker(pollInterval)
		tickChannel = ticker.C
		defer ticker.Stop()
	}
	var timer *time.Timer
	var idleChannel <-chan time.Time
	if idleTimeout > 0 {
		timer = time.NewTimer(idleTimeout)
		idleChannel = timer.C
		defer timer.Stop()
	}
	var errorBody []byte
	for {
		select {
		case chunk := <-results:
			if len(chunk.data) > 0 {
				if tick != nil && tick() {
					cancel()
					return errorBody, nil, true
				}
				consume(chunk.data)
				if len(errorBody) < maxErrorBodyBytes {
					remaining := maxErrorBodyBytes - len(errorBody)
					if len(chunk.data) > remaining {
						chunk.data = chunk.data[:remaining]
					}
					errorBody = append(errorBody, chunk.data...)
				}
				if timer != nil {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(idleTimeout)
				}
			}
			if chunk.err != nil {
				if errors.Is(chunk.err, io.EOF) {
					return errorBody, nil, false
				}
				return errorBody, chunk.err, false
			}
		case <-tickChannel:
			if tick() {
				cancel()
				return errorBody, nil, true
			}
		case <-idleChannel:
			cancel()
			return errorBody, fmt.Errorf("no response data for %s", idleTimeout), false
		case <-ctx.Done():
			cancel()
			return errorBody, ctx.Err(), errors.Is(ctx.Err(), context.Canceled)
		}
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.Trim(value, " \t\r\n\v\f")
	if value == "" {
		return 0
	}
	allDigits := true
	for _, char := range value {
		if char < '0' || char > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return 0
	}
	delay := date.Sub(now)
	if delay > maxRetryAfter {
		return maxRetryAfter
	}
	return delay
}

func (client *Client) httpClient(buffered bool) *http.Client {
	if client != nil && client.HTTP != nil {
		return client.HTTP
	}
	if buffered && client != nil && client.bufferedClient != nil {
		return client.bufferedClient
	}
	if !buffered && client != nil && client.stream != nil {
		return client.stream
	}
	return http.DefaultClient
}

func (client *Client) userAgent() string {
	if client != nil && client.UserAgent != "" {
		return client.UserAgent
	}
	return "gax/" + version.Value
}

func (client *Client) traceLogger() *wiretrace.Logger {
	if client != nil && client.trace != nil {
		return client.trace
	}
	return wiretrace.Default()
}
