package width

import "testing"

func TestDisplayWidthModesAndTerminalClamp(t *testing.T) {
	tests := []struct {
		name       string
		terminal   int
		mode       string
		configured int
		want       int
	}{
		{name: "auto floor is clamped on narrow terminals", terminal: 19, mode: "auto", want: 19},
		{name: "auto keeps width through tolerance", terminal: 110, mode: "auto", want: 110},
		{name: "auto caps wider terminals", terminal: 111, mode: "auto", want: 100},
		{name: "terminal mode keeps full width", terminal: 160, mode: "terminal", want: 160},
		{name: "terminal mode respects floor after clamp", terminal: 10, mode: "terminal", want: 10},
		{name: "exact width below terminal", terminal: 160, mode: "auto", configured: 60, want: 60},
		{name: "exact width clamps to terminal", terminal: 80, mode: "auto", configured: 120, want: 80},
		{name: "exact width with unknown terminal", configured: 120, want: 120},
		{name: "unknown terminal uses fallback width", mode: "auto", want: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ContentColumns(test.terminal, test.mode, test.configured); got != test.want {
				t.Fatalf("ContentColumns(%d, %q, %d) = %d, want %d", test.terminal, test.mode, test.configured, got, test.want)
			}
		})
	}
}

func TestAutoDisplayWidth(t *testing.T) {
	for _, test := range []struct{ terminal, want int }{
		{19, 20}, {20, 20}, {100, 100}, {101, 101}, {110, 110}, {111, 100},
	} {
		if got := AutoDisplayWidth(test.terminal); got != test.want {
			t.Errorf("AutoDisplayWidth(%d) = %d, want %d", test.terminal, got, test.want)
		}
	}
}

func TestWrapRowsUsesCellsAndKeepsContinuationIndent(t *testing.T) {
	if got := Cells("界a"); got != 3 {
		t.Fatalf("Cells() = %d, want 3", got)
	}
	rows := WrapRows("alpha beta", 2, 7)
	if len(rows) != 2 || rows[0] != "alpha" || rows[1] != "  beta" {
		t.Fatalf("WrapRows() = %#v, want [alpha, two-space beta]", rows)
	}
	rows = WrapRows("界界界", 0, 5)
	if len(rows) != 2 || rows[0] != "界界" || rows[1] != "界" {
		t.Fatalf("WrapRows() wide text = %#v", rows)
	}
}
