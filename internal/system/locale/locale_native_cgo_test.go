//go:build cgo && (linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package locale

import (
	"os"
	"testing"
)

func TestNativeProcessLocaleMatchesLocaleUtility(t *testing.T) {
	environment := os.Environ()
	probe := systemProbe()
	if probe == nil {
		t.Skip("locale utility is unavailable for comparison")
	}

	native, supported := nativeProcessLocale(environment)
	if !supported {
		t.Fatal("native libc locale detection is unavailable in this build")
	}
	if want := Detect(environment, probe); native != want {
		t.Fatalf("nativeProcessLocale() = %#v, environment probe = %#v", native, want)
	}
}
