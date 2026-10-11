package version

import "testing"

func TestValueMatchesPinnedUpstreamCheckout(t *testing.T) {
	if Value != "ff45833" {
		t.Fatalf("version = %q, want upstream git describe value ff45833", Value)
	}
}
