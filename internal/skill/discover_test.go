package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, directory, name, body string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverPrefersTheNearestSkillAndFallsBackToBuiltins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "customize-gax", "---\ndescription: project override\n---\n")
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "code-intelligence", "---\ndescription: custom code intel\n---\n")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "guide", "---\ndescription: shared guide\n---\n")
	writeSkill(t, filepath.Join(home, "config", "gax", "skills"), "deploy", "---\ndescription: deploy helper\n---\n")

	entries := Discover(Options{Home: home, CWD: project})
	byName := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	override, ok := byName["customize-gax"]
	if !ok || override.Description != "project override" || override.Body != "" {
		t.Fatalf("a discovered skill should shadow the built-in: %+v", override)
	}
	ciOverride, ok := byName["code-intelligence"]
	if !ok || ciOverride.Description != "custom code intel" || ciOverride.Body != "" {
		t.Fatalf("a discovered code-intelligence skill should shadow the built-in: %+v", ciOverride)
	}
	if got := byName["guide"].DisplayPath; got != filepath.FromSlash("~/.agents/skills/guide/SKILL.md") {
		t.Fatalf("shared skill display path = %q", got)
	}
	if got := byName["deploy"].Description; got != "deploy helper" {
		t.Fatalf("config skill description = %q", got)
	}
	for index := 1; index < len(entries); index++ {
		if entries[index-1].Name >= entries[index].Name {
			t.Fatalf("skills are not in name order: %q before %q", entries[index-1].Name, entries[index].Name)
		}
	}
}

func TestDiscoverSkipsDotDirectoriesAndDirectoriesWithoutADocument(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	root := filepath.Join(home, ".agents", "skills")
	writeSkill(t, root, ".hidden", "---\ndescription: hidden\n---\n")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	entries := Discover(Options{Home: home, CWD: t.TempDir()})
	for _, entry := range entries {
		if entry.Name == ".hidden" || entry.Name == "empty" {
			t.Fatalf("unusable skill was discovered: %+v", entry)
		}
	}
}

func TestEntryLoadReadsDiscoveredAndBuiltinDocuments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "guide", "---\ndescription: shared guide\n---\nfollow these steps\n")

	entries := Discover(Options{Home: home, CWD: t.TempDir()})
	byName := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	body, err := byName["guide"].Load()
	if err != nil || body != "---\ndescription: shared guide\n---\nfollow these steps\n" {
		t.Fatalf("discovered skill load = %q, err=%v", body, err)
	}
	builtin, err := byName["customize-gax"].Load()
	if err != nil || builtin != byName["customize-gax"].Body {
		t.Fatalf("built-in skill load = %d bytes, err=%v", len(builtin), err)
	}

	missing := byName["guide"]
	missing.Path = filepath.Join(home, "gone", "SKILL.md")
	if _, err := missing.Load(); err == nil {
		t.Fatal("loading a removed skill should fail")
	}
}

func TestDiscoverWithoutRootsStillOffersBuiltins(t *testing.T) {
	entries := Discover(Options{})
	found := false
	for _, entry := range entries {
		if entry.Name == "customize-gax" && entry.Path == "gax:skills/customize-gax/SKILL.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("built-in skill missing from %+v", entries)
	}
}

func TestDiscoverKeepsTheNearestRootAndIgnoresSharedSymlinkAliases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(project, "nested")
	writeSkill(t, filepath.Join(project, ".agents", "skills"), "shared", "---\ndescription: ancestor description\n---\n")
	writeSkill(t, filepath.Join(nested, ".agents", "skills"), "shared", "---\ndescription: nearest description\n---\n")

	byName := make(map[string]Entry)
	for _, entry := range Discover(Options{Home: home, CWD: nested}) {
		byName[entry.Name] = entry
	}
	if got := byName["shared"].Description; got != "nearest description" {
		t.Fatalf("nearest skill did not shadow the ancestor: %q", got)
	}

	sharedRoot := filepath.Join(home, ".agents", "skills")
	writeSkill(t, sharedRoot, "duplicate", "---\ndescription: shared skill\n---\n")
	writeSkill(t, filepath.Join(home, "config", "gax", "skills"), "duplicate", "---\ndescription: gax skill\n---\n")
	aliasProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(aliasProject, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sharedRoot, filepath.Join(aliasProject, ".agents", "skills")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, entry := range Discover(Options{Home: home, CWD: aliasProject}) {
		if entry.Name != "duplicate" {
			continue
		}
		if entry.Description != "gax skill" {
			t.Fatalf("a shared-root symlink shadowed the gax skill: %q", entry.Description)
		}
		return
	}
	t.Fatal("duplicate skill was not discovered")
}

func TestSkillDescriptionDecodesAndFoldsYamlScalars(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "plain comment", data: "---\ndescription: Build for AWS: ECS # deployment\n---\n", want: "Build for AWS: ECS"},
		{name: "folded block", data: "---\ndescription: >\n  Build reliable services.\n  Verify deployment safety.\n---\n", want: "Build reliable services. Verify deployment safety."},
		{name: "quoted continuation", data: "---\ndescription: 'it''s a\n  multi-line # description'\n---\n", want: "it's a multi-line # description"},
		{name: "escaped unicode", data: "---\ndescription: \"Use caf\\u00e9 and \\U0001F600\"\n---\n", want: "Use café and 😀"},
		{name: "absent", data: "---\nname: guide\n---\n", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := description([]byte(test.data)); got != test.want {
				t.Fatalf("skill description = %q, want %q", got, test.want)
			}
		})
	}
}
