package version

import "testing"

func TestValueDefaultsToDevelopmentBuild(t *testing.T) {
	if Value != "dev" {
		t.Fatalf("version = %q, want the dev default a plain go build reports", Value)
	}
}
