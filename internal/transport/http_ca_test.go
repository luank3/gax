package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientUsesCurlCABundleBeforeSSLOverrides(t *testing.T) {
	server := httpsCAMock(t, "trusted")
	defer server.Close()

	bundle := filepath.Join(t.TempDir(), "ca.pem")
	writeServerCert(t, bundle, server)
	t.Setenv("CURL_CA_BUNDLE", bundle)
	t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	t.Setenv("SSL_CERT_DIR", filepath.Join(t.TempDir(), "missing-certs"))

	client := NewClient()
	body, status, err := client.Get(context.Background(), server.URL, nil, time.Second, 1024, nil)
	if err != nil || status != http.StatusOK || string(body) != "trusted" {
		t.Fatalf("HTTPS request with CURL_CA_BUNDLE = (%q, %d), err=%v", body, status, err)
	}
}

func TestClientDoesNotFallBackFromUnreadableCurlCABundle(t *testing.T) {
	server := httpsCAMock(t, "trusted by SSL_CERT_FILE only")
	defer server.Close()

	certFile := filepath.Join(t.TempDir(), "ssl-root.pem")
	writeServerCert(t, certFile, server)
	t.Setenv("CURL_CA_BUNDLE", filepath.Join(t.TempDir(), "missing.pem"))
	t.Setenv("SSL_CERT_FILE", certFile)
	t.Setenv("SSL_CERT_DIR", "")

	client := NewClient()
	if _, _, err := client.Get(context.Background(), server.URL, nil, time.Second, 1024, nil); err == nil {
		t.Fatal("an unreadable CURL_CA_BUNDLE must take precedence over SSL_CERT_FILE")
	}
}

func TestCurlCABundleDoesNotReplaceHTTPSProxyTrust(t *testing.T) {
	var proxyRequests atomic.Int32
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	proxy.Config.ErrorLog = log.New(io.Discard, "", 0)

	bundle := filepath.Join(t.TempDir(), "proxy-only-ca.pem")
	writeServerCert(t, bundle, proxy)
	t.Setenv("CURL_CA_BUNDLE", bundle)
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("SSL_CERT_DIR", "")

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient()
	for _, httpClient := range []*http.Client{client.stream, client.bufferedClient} {
		roundTripper := httpClient.Transport.(proxyCAAwareRoundTripper)
		roundTripper.transport.Proxy = http.ProxyURL(proxyURL)
	}

	if _, _, err := client.Get(context.Background(), "https://origin.invalid/", nil, time.Second, 1024, nil); err == nil {
		t.Fatal("request through an untrusted HTTPS proxy unexpectedly succeeded")
	}
	if _, err := client.SSEPost(context.Background(), "https://origin.invalid/", nil, nil, time.Second, nil, nil); err == nil {
		t.Fatal("stream through an untrusted HTTPS proxy unexpectedly succeeded")
	}
	if got := proxyRequests.Load(); got != 0 {
		t.Fatalf("custom origin CA bundle trusted the HTTPS proxy; proxy received %d requests", got)
	}
}

func TestSSLCertOverridesDoNotReplaceHTTPSProxyTrust(t *testing.T) {
	if os.Getenv("GAX_TEST_CA_PROXY_CHILD") == "1" {
		if _, _, err := DefaultClient.Get(context.Background(), "https://origin.invalid/", nil,
			time.Second, 1024, nil); err == nil {
			t.Fatal("request through an untrusted HTTPS proxy unexpectedly succeeded")
		}
		return
	}
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly", "aix", "solaris":
	default:
		t.Skip("the Go platform root loader does not read SSL_CERT_FILE or SSL_CERT_DIR here")
	}

	for _, source := range []string{"file", "directory"} {
		t.Run(source, func(t *testing.T) {
			variable := "SSL_CERT_FILE"
			if source == "directory" {
				variable = "SSL_CERT_DIR"
			}
			marker := filepath.Join(t.TempDir(), "proxy-requested")
			proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = os.WriteFile(marker, []byte("CONNECT reached proxy"), 0600)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer proxy.Close()
			proxy.Config.ErrorLog = log.New(io.Discard, "", 0)

			sslCertFile, sslCertDir := "", ""
			if source == "file" {
				sslCertFile = filepath.Join(t.TempDir(), "origin-only-root.pem")
				writeServerCert(t, sslCertFile, proxy)
			} else {
				sslCertDir = t.TempDir()
				writeServerCert(t, filepath.Join(sslCertDir, "origin-only-root.pem"), proxy)
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestSSLCertOverridesDoNotReplaceHTTPSProxyTrust$")
			cmd.Env = appendEnvironment(os.Environ(), map[string]string{
				"CURL_CA_BUNDLE":          "",
				"SSL_CERT_FILE":           sslCertFile,
				"SSL_CERT_DIR":            sslCertDir,
				"HTTPS_PROXY":             proxy.URL,
				"https_proxy":             "",
				"HTTP_PROXY":              "",
				"http_proxy":              "",
				"ALL_PROXY":               "",
				"all_proxy":               "",
				"NO_PROXY":                "",
				"no_proxy":                "",
				"GAX_TEST_CA_PROXY_CHILD": "1",
			})
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("proxy child failed: %v\n%s", err, output)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("%s origin roots were used to trust the HTTPS proxy", variable)
			} else if !os.IsNotExist(err) {
				t.Fatalf("checking proxy marker: %v", err)
			}
		})
	}
}

func TestClientCombinesSSLCertFileAndDirectory(t *testing.T) {
	servers := []*httptest.Server{
		httpsCAMock(t, "file-root"),
		httpsCAMock(t, "directory-root"),
	}
	defer servers[0].Close()
	defer servers[1].Close()

	certFile := filepath.Join(t.TempDir(), "file-root.pem")
	writeServerCert(t, certFile, servers[0])
	certDir := t.TempDir()
	writeServerCert(t, filepath.Join(certDir, "directory-root.pem"), servers[1])
	t.Setenv("CURL_CA_BUNDLE", "")
	t.Setenv("SSL_CERT_FILE", certFile)
	t.Setenv("SSL_CERT_DIR", certDir)

	client := NewClient()
	wants := []string{"file-root", "directory-root"}
	for i, server := range servers {
		body, status, err := client.Get(context.Background(), server.URL, nil, time.Second, 1024, nil)
		if err != nil || status != http.StatusOK || string(body) != wants[i] {
			t.Errorf("HTTPS request %d with SSL_CERT_FILE and SSL_CERT_DIR = (%q, %d), err=%v",
				i, body, status, err)
		}
	}
}

func TestSSETLSFailureIncludesMissingCAAdvice(t *testing.T) {
	server := httpsCAMock(t, "untrusted")
	defer server.Close()
	client := &Client{
		HTTP: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: x509.NewCertPool(),
		}}},
		caStoreMissing: true,
	}

	response, err := client.SSEPost(context.Background(), server.URL, nil, nil, time.Second, nil, nil)
	if err == nil {
		t.Fatal("TLS verification should fail with an empty root pool")
	}
	const hint = "no CA certificate store found on this system; install the ca-certificates package or set SSL_CERT_FILE"
	if !strings.Contains(response.ErrorBody, hint) {
		t.Fatalf("TLS error body lacks missing CA advice: %q", response.ErrorBody)
	}
}

func TestBufferedTLSFailureIncludesMissingCAAdvice(t *testing.T) {
	server := httpsCAMock(t, "untrusted")
	defer server.Close()
	client := &Client{
		HTTP: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: x509.NewCertPool(),
		}}},
		caStoreMissing: true,
	}

	_, _, err := client.Get(context.Background(), server.URL, nil, time.Second, 1024, nil)
	if err == nil || !strings.Contains(err.Error(), missingCAStoreHint) {
		t.Fatalf("buffered TLS error = %v, want missing CA advice", err)
	}
}

func httpsCAMock(t *testing.T, response string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, response)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	return server
}

func writeServerCert(t *testing.T, path string, server *httptest.Server) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func appendEnvironment(environment []string, overrides map[string]string) []string {
	result := make([]string, 0, len(environment)+len(overrides))
	for _, variable := range environment {
		name, _, _ := strings.Cut(variable, "=")
		if _, overridden := overrides[name]; !overridden {
			result = append(result, variable)
		}
	}
	for name, value := range overrides {
		result = append(result, name+"="+value)
	}
	return result
}
