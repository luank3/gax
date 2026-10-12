// Package locale tracks whether terminal output can use UTF-8 and prepares child environments
// when a pinned LC_ALL would otherwise mask an available UTF-8 character locale.
package locale

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const probeTimeout = 250 * time.Millisecond

var utf8Candidates = []string{"C.UTF-8", "C.utf8", "UTF-8", "en_US.UTF-8"}

var pinnedCategories = []string{"LC_COLLATE", "LC_MONETARY", "LC_NUMERIC", "LC_TIME", "LC_MESSAGES"}

// State records whether the current process can render UTF-8 and which character locale should
// be passed to children. A nonempty ChildCType means the locale was selected as a fallback.
type State struct {
	UTF8         bool
	ChildrenUTF8 bool
	ChildCType   string
}

var (
	initializeMu           sync.Mutex
	stateMu                sync.RWMutex
	state                  State
	ready                  bool
	initializedEnvironment []string
)

// Detect determines locale capability using a charmap probe. The probe receives the environment
// to inspect and should return its character-map name, such as "UTF-8" or "ANSI_X3.4-1968".
// Candidate probing ignores LC_ALL because setlocale(LC_CTYPE, candidate) can select a ctype
// independently of that environment override in the current process.
func Detect(environment []string, probe func([]string) string) State {
	if probe == nil {
		return State{}
	}
	if isUTF8Charmap(probe(environment)) {
		return State{UTF8: true, ChildrenUTF8: true}
	}
	for _, candidate := range utf8Candidates {
		candidateEnvironment := setEnvironmentValue(environment, "LC_ALL", "")
		candidateEnvironment = setEnvironmentValue(candidateEnvironment, "LC_CTYPE", candidate)
		if !isUTF8Charmap(probe(candidateEnvironment)) {
			continue
		}
		all, _ := environmentValue(environment, "LC_ALL")
		return State{UTF8: true, ChildrenUTF8: all == "", ChildCType: candidate}
	}
	return State{}
}

// InitializeProcess detects the process locale and publishes a fallback LC_CTYPE to the process
// environment when no nonempty LC_ALL masks it. It returns true when no usable UTF-8 locale was
// found, allowing the caller to issue the compatibility warning once at startup.
func InitializeProcess() (unavailable bool) {
	initializeMu.Lock()
	defer initializeMu.Unlock()
	return initializeProcess()
}

func initializeProcess() (unavailable bool) {
	environment := os.Environ()
	current, native := nativeProcessLocale(environment)
	if !native {
		current = Detect(environment, systemProbe())
	}
	if current.ChildCType != "" && current.ChildrenUTF8 {
		_ = os.Setenv("LC_CTYPE", current.ChildCType)
	}
	processEnvironment := os.Environ()
	stateMu.Lock()
	state = current
	initializedEnvironment = processEnvironment
	ready = true
	stateMu.Unlock()
	return !current.UTF8
}

// Current returns the locale capability detected on first use or by the most recent
// InitializeProcess call.
func Current() State {
	stateMu.RLock()
	current, initialized := state, ready
	stateMu.RUnlock()
	if !initialized {
		initializeMu.Lock()
		stateMu.RLock()
		current, initialized = state, ready
		stateMu.RUnlock()
		if !initialized {
			initializeProcess()
			stateMu.RLock()
			current = state
			stateMu.RUnlock()
		}
		initializeMu.Unlock()
	}
	return current
}

// HaveUTF8 reports whether terminal decorations can safely use UTF-8 glyphs.
func HaveUTF8() bool { return Current().UTF8 }

// ChildEnvironment returns a copy of environment configured to use the selected UTF-8 ctype.
// If LC_ALL was pinned, it is cleared and its other categories are pinned individually so only
// the character encoding changes.
func (current State) ChildEnvironment(environment []string) []string {
	if !current.UTF8 || current.ChildCType == "" {
		return append([]string(nil), environment...)
	}
	all, _ := environmentValue(environment, "LC_ALL")
	childEnvironment := append([]string(nil), environment...)
	if all != "" {
		childEnvironment = setEnvironmentValue(childEnvironment, "LC_ALL", "")
		for _, category := range pinnedCategories {
			childEnvironment = setEnvironmentValue(childEnvironment, category, all)
		}
	}
	return setEnvironmentValue(childEnvironment, "LC_CTYPE", current.ChildCType)
}

// ChildEnvironment returns the process's preferred child environment.
func ChildEnvironment(environment []string) []string { return Current().ChildEnvironment(environment) }

// ForceUTF8ChildEnvironment probes the supplied environment and selects the first available
// UTF-8 ctype for a child when needed. It is useful for child environments that differ from the
// current process environment, such as test runners or commands with an explicit environment.
func ForceUTF8ChildEnvironment(environment []string) []string {
	stateMu.RLock()
	current, initialized := state, ready
	processEnvironment := append([]string(nil), initializedEnvironment...)
	stateMu.RUnlock()
	if initialized {
		if sameEnvironment(environment, processEnvironment) {
			return current.ChildEnvironment(environment)
		}
		return Detect(environment, systemProbe()).ChildEnvironment(environment)
	}
	if sameEnvironment(environment, os.Environ()) {
		return Current().ChildEnvironment(environment)
	}
	return Detect(environment, systemProbe()).ChildEnvironment(environment)
}

func sameEnvironment(left, right []string) bool {
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

func systemProbe() func([]string) string {
	localePath := findLocaleCommand()
	if localePath == "" {
		return nil
	}
	return func(environment []string) string {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		command := exec.CommandContext(ctx, localePath, "charmap")
		command.Env = environment
		output, commandErr := command.Output()
		if commandErr != nil {
			return ""
		}
		return strings.TrimSpace(string(output))
	}
}

func findLocaleCommand() string {
	if path, err := exec.LookPath("locale"); err == nil {
		return path
	}
	for _, path := range []string{"/usr/bin/locale", "/bin/locale", "/usr/local/bin/locale", "/opt/homebrew/bin/locale"} {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return ""
}

func isUTF8Charmap(charmap string) bool {
	return charmap == "UTF-8"
}

func environmentValue(environment []string, name string) (string, bool) {
	prefix := name + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix), true
		}
	}
	return "", false
}

func setEnvironmentValue(environment []string, name, value string) []string {
	prefix := name + "="
	updated := append([]string(nil), environment...)
	for index, entry := range updated {
		if strings.HasPrefix(entry, prefix) {
			updated[index] = prefix + value
			for duplicate := index + 1; duplicate < len(updated); {
				if strings.HasPrefix(updated[duplicate], prefix) {
					updated = append(updated[:duplicate], updated[duplicate+1:]...)
				} else {
					duplicate++
				}
			}
			return updated
		}
	}
	return append(updated, prefix+value)
}
