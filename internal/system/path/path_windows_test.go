//go:build windows

package path_test

import (
	"testing"

	systempath "github.com/luank3/gax/internal/system/path"
)

func TestWindowsPaths(t *testing.T) {
	for _, test := range []struct{ name, got, want string }{
		{"join", systempath.Join(`C:\Users\A B`, "missing/../file"), `C:\Users\A B\missing\..\file`},
		{"dir", systempath.Dir(`C:\Users\A B\file`), `C:\Users\A B`},
		{"parent", func() string { p, _ := systempath.ClimbToParent(`C:\Users\A B`); return p }(), `C:\Users`},
		{"expand-missing-home", systempath.ExpandHome(`~\file`, ""), `~\file`},
		{"expand", systempath.ExpandHome(`~\file`, `C:\Users\A B`), `C:\Users\A B\file`},
		{"collapse", systempath.CollapseHome(`c:\Users\A B\file`, `C:\Users\A B`), `~\file`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Errorf("%s = %q, want %q", test.name, test.got, test.want)
			}
		})
	}
	got, ok := systempath.Relativize(`C:\code\a b\file`, `c:\code\a b`)
	if !ok || got != `file` {
		t.Errorf("Relativize() = %q,%v, want file,true", got, ok)
	}
	if _, ok := systempath.Relativize(`C:\code\..\file`, `C:\code`); ok {
		t.Error("Relativize(parent traversal) succeeded")
	}
	if p, moved := systempath.ClimbToParent(`\\server\share\`); moved || p != `\\server\share\` {
		t.Errorf("ClimbToParent(UNC root) = %q,%v", p, moved)
	}
}

func TestWindowsHomeFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", `C:\Users\测试`)
	got, ok := systempath.XDGGaxPath("", "", ".config", "config.json")
	if !ok || got != `C:\Users\测试\.config\gax\config.json` {
		t.Errorf("XDGGaxPath() = %q,%v", got, ok)
	}
}
