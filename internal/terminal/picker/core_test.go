package picker

import "testing"

func TestMatchRequiresEverySpaceSeparatedCaseInsensitiveTerm(t *testing.T) {
	for _, test := range []struct {
		label string
		query string
		want  bool
	}{
		{"anthropic/claude-opus-4", "OPUS claude", true},
		{"anthropic/claude-opus-4", "anthropic 4", true},
		{"anthropic/claude-opus-4", "anthropic gpt", false},
		{"hello world", "  hello   world  ", true},
		{"hello world", "llowo", false},
		{"anything", "", true},
	} {
		if got := Match(test.label, test.query); got != test.want {
			t.Errorf("Match(%q, %q) = %t, want %t", test.label, test.query, got, test.want)
		}
	}
}

func TestCoreQueryNavigationAndViewportFollowMatches(t *testing.T) {
	core := NewCore(Options{Items: []Item{{Label: "alpha"}, {Label: "beta"}, {Label: "alphabet"}, {Label: "gamma"}}})
	core.SetViewportRows(2)
	core.Move(Next)
	if got := core.Selection(); got != 1 {
		t.Fatalf("selection after next = %d, want 1", got)
	}
	core.Page(Next)
	if got, first := core.Selection(), core.FirstVisible(); got != 2 || first != 1 {
		t.Fatalf("page selection/view = %d/%d, want 2/1", got, first)
	}
	core.SetQuery("ALPHA")
	if got, want := core.MatchIndices(), []int{0, 2}; !equalInts(got, want) {
		t.Fatalf("matches after query = %v, want %v", got, want)
	}
	if core.Selection() != 0 || core.FirstVisible() != 0 {
		t.Fatalf("query did not reset selection: selection=%d first=%d", core.Selection(), core.FirstVisible())
	}
	core.TruncateQuery(1)
	if got, want := core.MatchIndices(), []int{0, 1, 2, 3}; !equalInts(got, want) {
		t.Fatalf("matches after truncation = %v, want %v", got, want)
	}
}

func TestCoreNavigationClampsAndCentersSelection(t *testing.T) {
	items := make([]Item, 100)
	for index := range items {
		items[index].Label = "x"
	}
	core := NewCore(Options{Items: items})
	core.SetViewportRows(20)
	core.Page(Next)
	core.Page(Next)
	if got, first := core.Selection(), core.FirstVisible(); got != 20 || first != 10 {
		t.Fatalf("page selection/view = %d/%d, want 20/10", got, first)
	}
	core.SelectItem(50)
	if got, first := core.Selection(), core.FirstVisible(); got != 50 || first != 40 {
		t.Fatalf("centered selection/view = %d/%d, want 50/40", got, first)
	}
	core.SetViewportRows(0)
	core.SelectLast()
	if got, first := core.Selection(), core.FirstVisible(); got != 99 || first != 99 {
		t.Fatalf("zero-row selection/view = %d/%d, want 99/99", got, first)
	}
	core.Move(Next)
	if core.Selection() != 99 {
		t.Fatalf("selection moved past last item: %d", core.Selection())
	}
}

func TestPickerCellLayoutAccountsForDetailsAndCurrentTag(t *testing.T) {
	plain := Item{Label: "short"}
	if got := LabelCells(plain, 100); got != 98 {
		t.Fatalf("plain label cells = %d, want 98", got)
	}
	current := Item{Label: "short", Current: true}
	if got := LabelCells(current, 100); got != 87 {
		t.Fatalf("current label cells = %d, want 87", got)
	}
	long := Item{Label: "abcdefghijklmnopqrstuvwxyz", Detail: "3m ago"}
	if got := LabelCells(long, 30); got != 20 {
		t.Fatalf("detailed label cells = %d, want 20", got)
	}
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
