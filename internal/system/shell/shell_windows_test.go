//go:build windows

package shell

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWindowsFindUsesAbsolutePATHAndExecutableExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+`;.;relative`)
	t.Setenv("PATHEXT", ".EXE;.CMD")
	if got := Find("fixture"); got != path {
		t.Errorf("Find(fixture) = %q, want %q", got, path)
	}
	if got := Find(path); got != path {
		t.Errorf("Find(absolute exe) = %q, want %q", got, path)
	}
	t.Chdir(dir)
	t.Setenv("PATH", ".;relative")
	if got := Find("fixture"); got != "" {
		t.Errorf("Find(relative PATH) = %q, want empty", got)
	}
}

func TestWindowsCommandAdaptsShellArguments(t *testing.T) {
	for _, test := range []struct {
		name     string
		shell    string
		command  string
		wantArgs []string
	}{
		{
			name:     "bash",
			shell:    `C:\Program Files\Git\bin\bash.exe`,
			command:  "echo hello",
			wantArgs: []string{`C:\Program Files\Git\bin\bash.exe`, "-c", "echo hello"},
		},
		{
			name:     "powershell",
			shell:    `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
			command:  "Get-Process",
			wantArgs: []string{`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "-NoProfile", "-NonInteractive", "-Command", "Get-Process"},
		},
		{
			name:     "pwsh",
			shell:    `C:\Program Files\PowerShell\7\pwsh.exe`,
			command:  "Get-Process",
			wantArgs: []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoProfile", "-NonInteractive", "-Command", "Get-Process"},
		},
		{
			name:     "cmd",
			shell:    `C:\Windows\System32\cmd.exe`,
			command:  "dir",
			wantArgs: []string{`C:\Windows\System32\cmd.exe`, "/d", "/s", "/c", "dir"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := Command(test.shell, test.command)
			if !reflect.DeepEqual(cmd.Args, test.wantArgs) {
				t.Fatalf("Command(%q) args = %v, want %v", test.shell, cmd.Args, test.wantArgs)
			}
		})
	}
}

func TestWindowsResolveFallsBackToPowerShellOrCMD(t *testing.T) {
	dir := t.TempDir()
	psPath := filepath.Join(dir, "powershell.exe")
	if err := os.WriteFile(psPath, []byte("fake ps"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".EXE")
	t.Setenv("ProgramFiles", t.TempDir())
	t.Setenv("ProgramFiles(x86)", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())

	got, invalid := Resolve("")
	if got != psPath || invalid {
		t.Fatalf("Resolve() fallback = (%q, %t), want (%q, false)", got, invalid, psPath)
	}
}
