// Package cli parses gax command-line options and obtains a one-shot prompt.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/luank3/gax/internal/subagent"
)

type Action uint8

const (
	ActionRun Action = iota
	ActionHelp
	ActionVersion
)

type ResumeMode uint8

const (
	ResumeNone ResumeMode = iota
	ResumeLatest
	ResumeSelect
)

type Selection struct {
	Provider *string
	Model    *string
	Effort   *string
	Preset   *string
}

type Options struct {
	Action     Action
	Selection  Selection
	ResumeMode ResumeMode
	ResumeID   *string
	PromptArgs []string
	OneShot    bool
	Raw        bool
	JSON       bool
	Bare       bool
	NoSession  bool
}

// OptionError carries the diagnostic emitted by getopt for malformed option syntax.
type OptionError struct {
	message string
}

func (err *OptionError) Error() string { return err.message }

// Diagnostic formats the option-parser error with the invoked program name and usage hint.
func (err *OptionError) Diagnostic(program string) string {
	if program == "" {
		program = "gax"
	}
	return fmt.Sprintf("%s: %s\nTry 'gax --help' for usage.\n", program, err.message)
}

// CheckSubagentDepth rejects nested gax runs that reached the configured recursion limit.
func CheckSubagentDepth(value string) error { return subagent.CheckDepth(value) }

// Parse interprets arguments without the program name. The preset callback recognizes a bare
// first word as the preset shortcut; option parsing otherwise remains independent of
// config loading and process exit behavior.
func Parse(args []string, presetExists func(string) bool) (Options, error) {
	return parse(args, presetExists, nil)
}

// ParseWithPresets parses arguments while including available preset names in the diagnostic for
// an unknown leading word. presetExists may recognize malformed definitions too, matching the
// config layer's name ownership rule; presetNames should return only applicable presets.
func ParseWithPresets(args []string, presetExists func(string) bool, presetNames func() []string) (Options, error) {
	return parse(args, presetExists, presetNames)
}

func parse(args []string, presetExists func(string) bool, presetNames func() []string) (Options, error) {
	options := Options{Action: ActionRun}
	firstWord := ""
	if len(args) > 0 {
		firstWord = args[0]
	}
	leadingPreset := ""
	start := 0
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && presetExists != nil && presetExists(args[0]) {
		leadingPreset = args[0]
		start = 1
	}

	var sawContinue, sawResume bool
	var positional []string
	parseOptions := true
	for index := start; index < len(args); index++ {
		arg := args[index]
		if !parseOptions {
			positional = append(positional, arg)
			continue
		}
		if arg == "--" {
			parseOptions = false
			continue
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			optionName, value, hasValue := strings.Cut(arg[2:], "=")
			name, err := resolveLongOption(optionName, arg[2:], hasValue)
			if err != nil {
				return Options{}, err
			}
			if hasValue {
				switch name {
				case "help", "version", "print", "continue", "json", "raw", "bare", "no-session":
					return Options{}, &OptionError{message: fmt.Sprintf("option '--%s' doesn't allow an argument", name)}
				}
			}
			switch name {
			case "help":
				options.Action = ActionHelp
				return options, nil
			case "version":
				options.Action = ActionVersion
				return options, nil
			case "print":
				options.OneShot = true
			case "json":
				options.JSON, options.OneShot = true, true
			case "continue":
				sawContinue, options.ResumeMode = true, ResumeLatest
			case "resume":
				sawResume, options.ResumeMode = true, ResumeSelect
				if hasValue {
					options.ResumeID = stringPtr(value)
				}
			case "raw":
				options.Raw = true
			case "bare":
				options.Bare = true
			case "no-session":
				options.NoSession = true
			case "provider", "model", "effort", "preset":
				if !hasValue {
					if index+1 >= len(args) {
						return Options{}, &OptionError{message: fmt.Sprintf("option '--%s' requires an argument", name)}
					}
					index++
					value = args[index]
				}
				selection := stringPtr(value)
				switch name {
				case "provider":
					options.Selection.Provider = selection
				case "model":
					options.Selection.Model = selection
				case "effort":
					options.Selection.Effort = selection
				case "preset":
					options.Selection.Preset = selection
				}
			default:
				return Options{}, &OptionError{message: fmt.Sprintf("unrecognized option '--%s'", optionName)}
			}
			continue
		}
		for _, short := range arg[1:] {
			switch short {
			case 'h':
				options.Action = ActionHelp
				return options, nil
			case 'v':
				options.Action = ActionVersion
				return options, nil
			case 'p':
				options.OneShot = true
			case 'c':
				sawContinue, options.ResumeMode = true, ResumeLatest
			default:
				return Options{}, &OptionError{message: fmt.Sprintf("invalid option -- '%c'", short)}
			}
		}
	}

	if sawContinue && sawResume {
		return Options{}, errors.New("use only one of --continue / --resume")
	}
	if leadingPreset != "" && options.Selection.Preset != nil {
		return Options{}, errors.New("use only one of a leading preset name / --preset")
	}
	if leadingPreset != "" {
		options.Selection.Preset = stringPtr(leadingPreset)
	}
	for _, selection := range []struct {
		name  string
		value *string
	}{{"--provider=", options.Selection.Provider}, {"--model=", options.Selection.Model},
		{"--effort=", options.Selection.Effort}, {"--preset=", options.Selection.Preset}} {
		if selection.value != nil && *selection.value == "" {
			return Options{}, fmt.Errorf("%s requires a value", selection.name)
		}
	}
	if options.ResumeMode == ResumeSelect && options.ResumeID != nil && *options.ResumeID == "" {
		return Options{}, errors.New("--resume= requires a session id")
	}
	if options.OneShot && options.ResumeMode == ResumeSelect && options.ResumeID == nil {
		return Options{}, errors.New("-p with --resume requires a session id (e.g. --resume=ID)")
	}
	if !options.OneShot && len(positional) > 0 {
		if leadingPreset == "" && len(positional) == 1 && firstWord == positional[0] {
			known := ""
			if presetNames != nil {
				if names := presetNames(); len(names) > 0 {
					known = " (presets: " + strings.Join(names, ", ") + ")"
				}
			}
			return Options{}, fmt.Errorf("'%s' is not a preset%s\nA prompt needs -p / --print. Try 'gax --help' for usage.", firstWord, known)
		}
		return Options{}, errors.New("positional arguments require -p / --print\nTry 'gax --help' for usage.")
	}
	options.PromptArgs = positional
	return options, nil
}

// ReadPrompt follows gax's one-shot input rules. A nil prompt means interactive mode or a
// promptless resume that continues the recorded conversation.
func ReadPrompt(options Options, input io.Reader, inputIsTTY bool) (*string, error) {
	if !options.OneShot {
		return nil, nil
	}
	resuming := options.ResumeMode != ResumeNone
	var prompt string
	if len(options.PromptArgs) > 0 {
		prompt = strings.Join(options.PromptArgs, " ")
	} else if !inputIsTTY {
		if input == nil {
			return nil, errors.New("failed to read stdin")
		}
		data, err := io.ReadAll(input)
		if err != nil {
			return nil, errors.New("failed to read stdin")
		}
		prompt = string(data)
		if strings.HasSuffix(prompt, "\n") {
			prompt = strings.TrimSuffix(prompt, "\n")
			prompt = strings.TrimSuffix(prompt, "\r")
		}
	} else if resuming {
		return nil, nil
	} else {
		return nil, errors.New("-p requires a prompt (positional args or piped stdin)")
	}
	if prompt == "" {
		if resuming {
			return nil, nil
		}
		return nil, errors.New("-p prompt is empty")
	}
	return &prompt, nil
}

func stringPtr(value string) *string { return &value }

func resolveLongOption(name, spelling string, hasValue bool) (string, error) {
	options := [...]string{
		"help", "version", "print", "continue", "resume", "no-session", "json", "raw", "bare",
		"provider", "model", "effort", "preset",
	}
	for _, option := range options {
		if name == option {
			return option, nil
		}
	}
	if name == "" && !hasValue {
		return "", &OptionError{message: "unrecognized option '--'"}
	}
	matches := make([]string, 0, 2)
	for _, option := range options {
		if strings.HasPrefix(option, name) {
			matches = append(matches, option)
		}
	}
	if len(matches) == 0 {
		return "", &OptionError{message: fmt.Sprintf("unrecognized option '--%s'", spelling)}
	}
	if len(matches) > 1 {
		possibilities := make([]string, len(matches))
		for index, match := range matches {
			possibilities[index] = "'--" + match + "'"
		}
		return "", &OptionError{message: fmt.Sprintf("option '--%s' is ambiguous; possibilities: %s",
			spelling, strings.Join(possibilities, " "))}
	}
	return matches[0], nil
}
