package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestCA(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gax transport test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

// HTTPS proxies verify against the machine store, so origin-only CA overrides must not leak into it.
// The assertion is only meaningful while this test is the first caller of systemProxyRootCAs in the
// process, which the alphabetical file order keeps true for the transport package.
func TestSystemProxyRootsIgnoreOriginCAOverrides(t *testing.T) {
	certificate := newTestCA(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "override.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", path)
	t.Setenv("SSL_CERT_DIR", directory)

	roots := systemProxyRootCAs()
	if roots == nil {
		t.Fatal("system proxy roots are nil")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots}); err == nil {
		t.Fatal("proxy verification trusts the SSL_CERT_FILE override")
	}
	if again := systemProxyRootCAs(); again != roots {
		t.Fatal("system proxy roots should be loaded once per process")
	}
}

func TestUnixSystemProxyRootsAlwaysReturnsAPool(t *testing.T) {
	if roots := unixSystemProxyRootCAs(); roots == nil {
		t.Fatal("unix proxy roots are nil")
	}
}
