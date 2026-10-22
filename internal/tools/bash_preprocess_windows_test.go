//go:build windows

package tools

import "testing"

func TestWindowsBashCDPrefix(t *testing.T) {
	command := `cd 'C:/Users/A B/project' && printf ok`
	offset := stripBashCDPrefix(command, `C:\Users\A B\project`, "")
	if offset == 0 || command[offset:] != "printf ok" {
		t.Errorf("stripBashCDPrefix(%q) = %d, want prefix removed", command, offset)
	}
	if got := stripBashCDPrefix(`cd C:\Users\project && printf ok`, `C:\Users\project`, ""); got != 0 {
		t.Errorf("stripBashCDPrefix(unescaped backslashes) = %d, want 0", got)
	}
}
