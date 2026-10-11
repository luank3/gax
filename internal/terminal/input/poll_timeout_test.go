package input

import (
	"testing"
	"time"
)

func TestPollTimeoutMillisRoundsPositiveSubmillisecondWaitsUp(t *testing.T) {
	for _, test := range []struct {
		name string
		wait time.Duration
		want int
	}{
		{name: "no wait", wait: 0, want: 0},
		{name: "negative wait", wait: -time.Millisecond, want: 0},
		{name: "submillisecond wait", wait: 500 * time.Microsecond, want: 1},
		{name: "exact millisecond", wait: time.Millisecond, want: 1},
		{name: "escape timeout", wait: 50 * time.Millisecond, want: 50},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := pollTimeoutMillis(test.wait); got != test.want {
				t.Fatalf("poll timeout for %s = %dms, want %dms", test.wait, got, test.want)
			}
		})
	}
}
