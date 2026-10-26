//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package picker

import (
	"testing"
	"time"
)

func TestPickerPollTimeoutMillisBlocksIndefinitelyForZeroOrNegative(t *testing.T) {
	for _, test := range []struct {
		name string
		wait time.Duration
		want int
	}{
		{name: "zero wait", wait: 0, want: -1},
		{name: "negative wait", wait: -time.Millisecond, want: -1},
		{name: "submillisecond wait", wait: 500 * time.Microsecond, want: 1},
		{name: "exact millisecond", wait: time.Millisecond, want: 1},
		{name: "ten milliseconds", wait: 10 * time.Millisecond, want: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := pickerPollTimeoutMillis(test.wait); got != test.want {
				t.Fatalf("pickerPollTimeoutMillis(%s) = %d, want %d", test.wait, got, test.want)
			}
		})
	}
}
