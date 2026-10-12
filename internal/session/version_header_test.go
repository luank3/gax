package session

import (
	"testing"

	"github.com/luank3/gax/internal/version"
)

// A release build injects the version with -ldflags, so the header has to carry whatever the
// running binary was built with rather than a value baked in at compile time.
func TestNewHeaderRecordCarriesTheInjectedVersion(t *testing.T) {
	original := version.Value
	version.Value = "v9.9.9-test"
	defer func() { version.Value = original }()

	if got := NewHeaderRecord(HeaderInput{}).GaxVersion; got != "v9.9.9-test" {
		t.Fatalf("header gax_version = %q, want the injected version", got)
	}
}
