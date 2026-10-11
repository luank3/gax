package cli_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/luank3/gax/internal/cli"
)

func TestParseOneShotFlagsAndInterspersedPrompt(t *testing.T) {
	options, err := cli.Parse([]string{
		"hello", "-p", "--provider=openai", "world", "--json", "--model", "gpt-test",
		"--effort=high", "--raw", "--bare", "--no-session", "--preset", "focused",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.Action != cli.ActionRun || !options.OneShot || !options.JSON || !options.Raw || !options.Bare || !options.NoSession {
		t.Fatalf("flags did not resolve: %+v", options)
	}
	if value(options.Selection.Provider) != "openai" || value(options.Selection.Model) != "gpt-test" ||
		value(options.Selection.Effort) != "high" || value(options.Selection.Preset) != "focused" {
		t.Fatalf("selection flags did not resolve: %+v", options.Selection)
	}
	if got := options.PromptArgs; len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("interspersed prompt arguments were not preserved: %#v", got)
	}
}

func TestParseLeadingPresetAndConflicts(t *testing.T) {
	isPreset := func(name string) bool { return name == "review" }
	options, err := cli.Parse([]string{"review", "-p", "check this"}, isPreset)
	if err != nil {
		t.Fatal(err)
	}
	if value(options.Selection.Preset) != "review" || len(options.PromptArgs) != 1 || options.PromptArgs[0] != "check this" {
		t.Fatalf("leading preset was not consumed: %+v", options)
	}
	if _, err := cli.Parse([]string{"review", "--preset=fast"}, isPreset); err == nil {
		t.Fatal("leading preset and --preset should conflict")
	}
}

func TestParseRejectsInvalidSelectionResumeAndPositionalForms(t *testing.T) {
	cases := [][]string{
		{"--provider="},
		{"--model="},
		{"--effort="},
		{"--preset="},
		{"--resume="},
		{"-p", "--resume"},
		{"--continue", "--resume=id"},
		{"prompt"},
		{"--unknown"},
	}
	for _, args := range cases {
		if _, err := cli.Parse(args, nil); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", args)
		}
	}
	options, err := cli.Parse([]string{"-p", "--resume=id"}, nil)
	if err != nil || options.ResumeID == nil || *options.ResumeID != "id" {
		t.Fatalf("explicit resume id should be valid for one-shot: options=%+v err=%v", options, err)
	}
}

func value(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestParseHelpAndVersionActions(t *testing.T) {
	for _, test := range []struct {
		args []string
		want cli.Action
	}{{[]string{"--help"}, cli.ActionHelp}, {[]string{"-v"}, cli.ActionVersion}} {
		options, err := cli.Parse(test.args, nil)
		if err != nil || options.Action != test.want {
			t.Errorf("Parse(%q): options=%+v err=%v", test.args, options, err)
		}
	}
}

func TestReadPromptJoinsArgumentsAndReadsPipedInput(t *testing.T) {
	prompt, err := cli.ReadPrompt(cli.Options{OneShot: true, PromptArgs: []string{"fix", "this"}}, strings.NewReader("ignored"), true)
	if err != nil || prompt == nil || *prompt != "fix this" {
		t.Fatalf("argument prompt: got %v err=%v", prompt, err)
	}
	prompt, err = cli.ReadPrompt(cli.Options{OneShot: true}, strings.NewReader("hello from stdin\r\n"), false)
	if err != nil || prompt == nil || *prompt != "hello from stdin" {
		t.Fatalf("stdin prompt: got %v err=%v", prompt, err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestReadPromptRejectsEmptyFreshInputButAllowsPromptlessResume(t *testing.T) {
	if _, err := cli.ReadPrompt(cli.Options{OneShot: true}, strings.NewReader("\n"), false); err == nil {
		t.Fatal("empty fresh prompt should be rejected")
	}
	prompt, err := cli.ReadPrompt(cli.Options{OneShot: true, ResumeMode: cli.ResumeSelect}, strings.NewReader(""), true)
	if err != nil || prompt != nil {
		t.Fatalf("promptless resume should be allowed: prompt=%v err=%v", prompt, err)
	}
	prompt, err = cli.ReadPrompt(cli.Options{OneShot: true, ResumeMode: cli.ResumeLatest}, strings.NewReader(""), false)
	if err != nil || prompt != nil {
		t.Fatalf("empty piped input on resume should be allowed: prompt=%v err=%v", prompt, err)
	}
	if _, err := cli.ReadPrompt(cli.Options{OneShot: true}, failingReader{}, false); err == nil {
		t.Fatal("stdin read failure should be returned")
	}
}

func TestParseRejectsArgumentsOnBooleanOptions(t *testing.T) {
	for _, arg := range []string{"--help=yes", "--version=yes", "--print=yes", "--continue=yes", "--json=yes", "--raw=yes", "--bare=yes", "--no-session=yes"} {
		if _, err := cli.Parse([]string{arg}, nil); err == nil {
			t.Errorf("Parse(%q) unexpectedly accepted a value", arg)
		}
	}
}

func TestReadPromptRemovesOnlyOneFinalNewline(t *testing.T) {
	prompt, err := cli.ReadPrompt(cli.Options{OneShot: true}, strings.NewReader("first\n\n"), false)
	if err != nil || prompt == nil || *prompt != "first\n" {
		t.Fatalf("prompt newline trimming differs: prompt=%q err=%v", promptValue(prompt), err)
	}
}

func promptValue(prompt *string) string {
	if prompt == nil {
		return "<nil>"
	}
	return *prompt
}

func TestParseResumeModes(t *testing.T) {
	latest, err := cli.Parse([]string{"--continue"}, nil)
	if err != nil || latest.ResumeMode != cli.ResumeLatest {
		t.Fatalf("continue mode: options=%+v err=%v", latest, err)
	}
	selected, err := cli.Parse([]string{"--resume=abc123"}, nil)
	if err != nil || selected.ResumeMode != cli.ResumeSelect || value(selected.ResumeID) != "abc123" {
		t.Fatalf("explicit resume mode: options=%+v err=%v", selected, err)
	}
	picker, err := cli.Parse([]string{"--resume"}, nil)
	if err != nil || picker.ResumeMode != cli.ResumeSelect || picker.ResumeID != nil {
		t.Fatalf("picker resume mode: options=%+v err=%v", picker, err)
	}
}

func TestParseLeadingPresetOnlyUsesFirstWord(t *testing.T) {
	isPreset := func(name string) bool { return name == "review" }
	interactive, err := cli.Parse([]string{"review"}, isPreset)
	if err != nil || value(interactive.Selection.Preset) != "review" || interactive.OneShot {
		t.Fatalf("interactive leading preset: options=%+v err=%v", interactive, err)
	}
	prompt, err := cli.Parse([]string{"-p", "review", "fix"}, isPreset)
	if err != nil || prompt.Selection.Preset != nil || len(prompt.PromptArgs) != 2 {
		t.Fatalf("later preset word must remain prompt text: options=%+v err=%v", prompt, err)
	}
}

func TestParseUnknownLeadingWordListsKnownPresets(t *testing.T) {
	knownPresetNames := func() []string { return []string{"fast", "review"} }
	_, err := cli.ParseWithPresets([]string{"unknown"}, func(string) bool { return false }, knownPresetNames)
	want := "'unknown' is not a preset (presets: fast, review)\nA prompt needs -p / --print. Try 'gax --help' for usage."
	if err == nil || err.Error() != want {
		t.Fatalf("unknown leading word error = %v, want %q", err, want)
	}
	calls := 0
	if _, err := cli.ParseWithPresets([]string{"-p", "hello"}, func(string) bool { return false }, func() []string {
		calls++
		return nil
	}); err != nil || calls != 0 {
		t.Fatalf("preset names were loaded for an ordinary prompt: err=%v calls=%d", err, calls)
	}
}

func TestParseDoubleDashEndsOptionScanning(t *testing.T) {
	options, err := cli.Parse([]string{"-p", "--", "--not-an-option", "-v"}, nil)
	if err != nil || options.Action != cli.ActionRun || len(options.PromptArgs) != 2 ||
		options.PromptArgs[0] != "--not-an-option" || options.PromptArgs[1] != "-v" {
		t.Fatalf("double dash did not preserve prompt text: options=%+v err=%v", options, err)
	}
}

func TestParseSupportsUniqueLongOptionPrefixes(t *testing.T) {
	options, err := cli.Parse([]string{"--print", "--prov", "openai", "--ver"}, nil)
	if err != nil || options.Action != cli.ActionVersion || value(options.Selection.Provider) != "openai" {
		t.Fatalf("version option should terminate parsing before later validation: options=%+v err=%v", options, err)
	}
	options, err = cli.Parse([]string{"--prov", "openai", "--pr"}, nil)
	if err == nil {
		t.Fatal("--pr is ambiguous between print and preset")
	}
	options, err = cli.Parse([]string{"--prov", "openai", "--ver"}, nil)
	if err != nil || options.Action != cli.ActionVersion {
		t.Fatalf("unique --ver prefix should select version: options=%+v err=%v", options, err)
	}
	options, err = cli.Parse([]string{"--res=abc"}, nil)
	if err != nil || options.ResumeMode != cli.ResumeSelect || value(options.ResumeID) != "abc" {
		t.Fatalf("unique --res prefix should select resume: options=%+v err=%v", options, err)
	}
}

func TestParseLongOptionErrorsMatchGetoptDiagnostics(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"--h=1"}, want: "option '--help' doesn't allow an argument"},
		{args: []string{"--p=x"}, want: "option '--p=x' is ambiguous; possibilities: '--print' '--provider' '--preset'"},
		{args: []string{"--x=y"}, want: "unrecognized option '--x=y'"},
		{args: []string{"--=x"}, want: "option '--=x' is ambiguous; possibilities: '--help' '--version' '--print' '--continue' '--resume' '--no-session' '--json' '--raw' '--bare' '--provider' '--model' '--effort' '--preset'"},
		{args: []string{"---=x"}, want: "unrecognized option '---=x'"},
	} {
		if _, err := cli.Parse(test.args, nil); err == nil || err.Error() != test.want {
			t.Errorf("Parse(%q) error = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestCheckSubagentDepthMatchesRecursionCap(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "0", want: false},
		{value: "2", want: false},
		{value: "3", want: true},
		{value: "invalid", want: true},
		{value: "-1", want: true},
		{value: "2147483648", want: true},
		{value: " 2", want: false},
		{value: "2 ", want: true},
	} {
		err := cli.CheckSubagentDepth(test.value)
		if (err != nil) != test.want {
			t.Errorf("CheckSubagentDepth(%q) error = %v, want error %t", test.value, err, test.want)
		}
	}
}
