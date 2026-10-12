package transport

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var (
	proxyRootsOnce sync.Once
	proxyRoots     *x509.CertPool
)

// systemProxyRootCAs loads the machine trust store without the origin-only CA environment
// overrides. Go's Unix root loader reads SSL_CERT_FILE and SSL_CERT_DIR even when a TLS config
// leaves RootCAs nil, while the ported transport keeps HTTPS proxy verification on the system store.
func systemProxyRootCAs() *x509.CertPool {
	proxyRootsOnce.Do(func() {
		switch runtime.GOOS {
		case "aix", "android", "dragonfly", "freebsd", "linux", "netbsd", "openbsd", "solaris":
			proxyRoots = unixSystemProxyRootCAs()
		default:
			proxyRoots, _ = x509.SystemCertPool()
			if proxyRoots == nil {
				proxyRoots = x509.NewCertPool()
			}
		}
	})
	return proxyRoots
}

func unixSystemProxyRootCAs() *x509.CertPool {
	roots := x509.NewCertPool()
	for _, path := range []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/ca-bundle.pem",
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
		"/usr/local/share/certs/ca-root-nss.crt",
		"/etc/ssl/cert.pem",
	} {
		data, err := os.ReadFile(path)
		if err == nil {
			roots.AppendCertsFromPEM(data)
			break
		}
	}
	directories := []string{"/etc/ssl/certs", "/etc/pki/tls/certs"}
	if runtime.GOOS == "android" {
		directories = append(directories, "/system/etc/security/cacerts", "/data/misc/keychain/certs-added")
	}
	for _, directory := range directories {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err == nil {
				roots.AppendCertsFromPEM(data)
			}
		}
	}
	return roots
}
