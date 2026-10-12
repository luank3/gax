package path_test

import (
	"testing"

	systempath "github.com/luank3/gax/internal/system/path"
)

func TestJoinMatchesPathRules(t *testing.T) {
	for _, test := range []struct {
		base, suffix, want string
	}{
		{base: "/tmp", suffix: "foo", want: "/tmp/foo"},
		{base: "/var/tmp///", suffix: "foo", want: "/var/tmp/foo"},
		{base: "/tmp", suffix: "/foo", want: "/tmp/foo"},
		{base: "/", suffix: "etc", want: "/etc"},
		{base: "/", suffix: "/etc", want: "/etc"},
		{base: "subdir", suffix: "file.txt", want: "subdir/file.txt"},
		{base: ".", suffix: "file.txt", want: "./file.txt"},
		{base: "", suffix: "foo", want: "/foo"},
		{base: "/tmp", suffix: "", want: "/tmp/"},
		{base: "/tmp", suffix: "missing/../target", want: "/tmp/missing/../target"},
	} {
		t.Run(test.base+"|"+test.suffix, func(t *testing.T) {
			if got := systempath.Join(test.base, test.suffix); got != test.want {
				t.Fatalf("Join(%q, %q) = %q, want %q", test.base, test.suffix, got, test.want)
			}
		})
	}
}

func TestExpandHomeMatchesPathRules(t *testing.T) {
	for _, test := range []struct {
		path, home, want string
	}{
		{path: "/absolute/path", home: "/tmp/fake", want: "/absolute/path"},
		{path: "~", home: "/tmp/fake", want: "/tmp/fake"},
		{path: "~/sub/file", home: "/tmp/fake", want: "/tmp/fake/sub/file"},
		{path: "~/sub/file", home: "/tmp/fake/", want: "/tmp/fake/sub/file"},
		{path: "~/etc/hosts", home: "/", want: "/etc/hosts"},
		{path: "~/foo", home: "", want: "~/foo"},
		{path: "~root/etc", home: "/tmp/fake", want: "~root/etc"},
		{path: "~/missing/../prompt", home: "/tmp/fake", want: "/tmp/fake/missing/../prompt"},
	} {
		t.Run(test.path+"|"+test.home, func(t *testing.T) {
			if got := systempath.ExpandHome(test.path, test.home); got != test.want {
				t.Fatalf("ExpandHome(%q, %q) = %q, want %q", test.path, test.home, got, test.want)
			}
		})
	}
}

func TestCollapseHomeMatchesPathRules(t *testing.T) {
	for _, test := range []struct {
		path, home, want string
	}{
		{path: "/Users/alice/source/gax", home: "/Users/alice", want: "~/source/gax"},
		{path: "/Users/alice", home: "/Users/alice", want: "~"},
		{path: "/etc/hosts", home: "/Users/alice", want: "/etc/hosts"},
		{path: "/Users/alice2/x", home: "/Users/alice", want: "/Users/alice2/x"},
		{path: "/Users/alice/foo", home: "/Users/alice/", want: "~/foo"},
		{path: "/Users/alice/foo", home: "", want: "/Users/alice/foo"},
		{path: "/etc/hosts", home: "/", want: "~/etc/hosts"},
		{path: "/", home: "/", want: "~"},
	} {
		t.Run(test.path+"|"+test.home, func(t *testing.T) {
			if got := systempath.CollapseHome(test.path, test.home); got != test.want {
				t.Fatalf("CollapseHome(%q, %q) = %q, want %q", test.path, test.home, got, test.want)
			}
		})
	}
}

func TestRelativizeMatchesBoundaries(t *testing.T) {
	for _, test := range []struct {
		path, cwd, want string
		ok              bool
	}{
		{path: "/home/u/proj/src/x.c", cwd: "/home/u/proj", want: "src/x.c", ok: true},
		{path: "/home/u/proj/x.c", cwd: "/home/u/proj/", want: "x.c", ok: true},
		{path: "/etc/hosts", cwd: "/", want: "etc/hosts", ok: true},
		{path: "/repo/a..b/file", cwd: "/repo", want: "a..b/file", ok: true},
		{path: "/home/u/proj", cwd: "/home/u/proj", ok: false},
		{path: "/home/u/proj2/x", cwd: "/home/u/proj", ok: false},
		{path: "/repo/a/../b/file", cwd: "/repo", ok: false},
		{path: "src/x.c", cwd: "/home/u/proj", ok: false},
		{path: "/home/u/proj/x.c", cwd: "home/u/proj", ok: false},
	} {
		t.Run(test.path+"|"+test.cwd, func(t *testing.T) {
			got, ok := systempath.Relativize(test.path, test.cwd)
			if got != test.want || ok != test.ok {
				t.Fatalf("Relativize(%q, %q) = %q, %t; want %q, %t", test.path, test.cwd, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestClimbToParentStopsAtRoot(t *testing.T) {
	for _, test := range []struct {
		path, want string
		ok         bool
	}{
		{path: "/a/b", want: "/a", ok: true},
		{path: "/a", want: "/", ok: true},
		{path: "/", want: "/", ok: false},
		{path: "a/b", want: "a", ok: true},
		{path: "a", want: "a", ok: false},
	} {
		if got, ok := systempath.ClimbToParent(test.path); got != test.want || ok != test.ok {
			t.Errorf("ClimbToParent(%q) = %q, %t; want %q, %t", test.path, got, ok, test.want, test.ok)
		}
	}
}

func TestDirMatchesPOSIXParentRulesWithoutCleaning(t *testing.T) {
	for _, test := range []struct {
		path, want string
	}{
		{path: "/tmp/file", want: "/tmp"},
		{path: "/tmp/a/../file", want: "/tmp/a/.."},
		{path: "relative/file", want: "relative"},
		{path: "file", want: "."},
		{path: "/", want: "/"},
		{path: "relative/", want: "."},
	} {
		if got := systempath.Dir(test.path); got != test.want {
			t.Errorf("Dir(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestXDGGaxPathPreservesFormatting(t *testing.T) {
	for _, test := range []struct {
		base, home, homeRelative, relative, want string
		ok                                       bool
	}{
		{base: "/config", home: "/home/alice", homeRelative: ".config", relative: "config.json", want: "/config/gax/config.json", ok: true},
		{base: "/config/", home: "/home/alice", homeRelative: ".config", relative: "nested/../config.json", want: "/config//gax/nested/../config.json", ok: true},
		{base: "", home: "/home/alice", homeRelative: ".config", relative: "config.json", want: "/home/alice/.config/gax/config.json", ok: true},
		{base: "", home: "", homeRelative: ".config", relative: "config.json", ok: false},
	} {
		got, ok := systempath.XDGGaxPath(test.base, test.home, test.homeRelative, test.relative)
		if got != test.want || ok != test.ok {
			t.Errorf("XDGGaxPath(%q, %q, %q, %q) = %q, %t; want %q, %t",
				test.base, test.home, test.homeRelative, test.relative, got, ok, test.want, test.ok)
		}
	}
}
