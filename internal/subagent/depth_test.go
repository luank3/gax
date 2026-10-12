package subagent

import "testing"

func TestNextChildDepthMatchesRecordedStamp(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{value: "", want: 1},
		{value: "0", want: 1},
		{value: "2", want: 3},
		{value: "3", want: 3},
		{value: "9", want: 9},
		{value: "invalid", want: MaxDepth},
		{value: "-1", want: MaxDepth},
		{value: "2147483648", want: MaxDepth},
		{value: " 2", want: 3},
		{value: "2 ", want: MaxDepth},
	} {
		if got := NextChildDepth(test.value); got != test.want {
			t.Errorf("NextChildDepth(%q) = %d, want %d", test.value, got, test.want)
		}
	}
}
