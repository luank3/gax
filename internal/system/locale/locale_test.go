package locale

import (
	"reflect"
	"testing"
)

func TestCharmapMatchesUTF8Check(t *testing.T) {
	if !isUTF8Charmap("UTF-8") {
		t.Fatal("the UTF-8 codeset spelling was not accepted")
	}
	for _, charmap := range []string{"UTF8", "utf-8", " UTF-8 ", "ANSI_X3.4-1968"} {
		if isUTF8Charmap(charmap) {
			t.Errorf("isUTF8Charmap(%q) = true, want false", charmap)
		}
	}
}

func TestDetectUsesEffectiveLocaleAndCandidateFallback(t *testing.T) {
	tests := []struct {
		name     string
		env      []string
		charmaps map[string]string
		want     State
		probes   []string
	}{
		{
			name:     "effective LC_ALL is UTF-8",
			env:      []string{"LANG=C", "LC_CTYPE=C", "LC_ALL=en_US.UTF-8"},
			charmaps: map[string]string{"LC_ALL=en_US.UTF-8": "UTF-8"},
			want:     State{UTF8: true, ChildrenUTF8: true},
			probes:   []string{"LC_ALL=en_US.UTF-8"},
		},
		{
			name: "LC_CTYPE takes precedence over LANG",
			env:  []string{"LANG=C", "LC_CTYPE=en_US.UTF-8"},
			charmaps: map[string]string{
				"LC_ALL=|LC_CTYPE=en_US.UTF-8": "UTF-8",
			},
			want:   State{UTF8: true, ChildrenUTF8: true},
			probes: []string{"LC_ALL=|LC_CTYPE=en_US.UTF-8"},
		},
		{
			name: "LC_ALL masks a UTF-8 LC_CTYPE; candidate is available to this process",
			env:  []string{"LANG=C", "LC_CTYPE=C", "LC_ALL=C", "LC_COLLATE=fr_FR"},
			charmaps: map[string]string{
				"LC_ALL=C":                 "ANSI_X3.4-1968",
				"LC_ALL=|LC_CTYPE=C.UTF-8": "UTF-8",
			},
			want:   State{UTF8: true, ChildCType: "C.UTF-8"},
			probes: []string{"LC_ALL=C", "LC_ALL=|LC_CTYPE=C.UTF-8"},
		},
		{
			name: "unavailable candidates preserve ASCII fallback",
			env:  []string{"LANG=C", "LC_ALL=C"},
			charmaps: map[string]string{
				"LC_ALL=C": "ANSI_X3.4-1968",
			},
			want: State{},
			probes: []string{
				"LC_ALL=C", "LC_ALL=|LC_CTYPE=C.UTF-8", "LC_ALL=|LC_CTYPE=C.utf8",
				"LC_ALL=|LC_CTYPE=UTF-8", "LC_ALL=|LC_CTYPE=en_US.UTF-8",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var probes []string
			got := Detect(test.env, func(env []string) string {
				key := effectiveLocaleKey(env)
				probes = append(probes, key)
				return test.charmaps[key]
			})
			if got != test.want {
				t.Fatalf("Detect() = %#v, want %#v", got, test.want)
			}
			if !reflect.DeepEqual(probes, test.probes) {
				t.Fatalf("probe order = %v, want %v", probes, test.probes)
			}
		})
	}
}

func effectiveLocaleKey(environment []string) string {
	all, _ := environmentValue(environment, "LC_ALL")
	if all != "" {
		return "LC_ALL=" + all
	}
	ctype, _ := environmentValue(environment, "LC_CTYPE")
	return "LC_ALL=|LC_CTYPE=" + ctype
}

func TestChildEnvironmentPinsOtherCategoriesWhenClearingLCAll(t *testing.T) {
	state := State{UTF8: true, ChildCType: "UTF-8"}
	environment := []string{
		"PATH=/bin", "LC_ALL=fr_FR", "LC_CTYPE=C", "LC_COLLATE=de_DE", "LC_MONETARY=de_DE",
		"LC_NUMERIC=de_DE", "LC_TIME=de_DE", "LC_MESSAGES=de_DE",
	}
	want := []string{
		"PATH=/bin", "LC_ALL=", "LC_CTYPE=UTF-8", "LC_COLLATE=fr_FR", "LC_MONETARY=fr_FR",
		"LC_NUMERIC=fr_FR", "LC_TIME=fr_FR", "LC_MESSAGES=fr_FR",
	}
	if got := state.ChildEnvironment(environment); !reflect.DeepEqual(got, want) {
		t.Fatalf("ChildEnvironment() = %v, want %v", got, want)
	}
}

func TestChildEnvironmentSetsOnlyCTypeWhenLCAllIsNotPinned(t *testing.T) {
	state := State{UTF8: true, ChildrenUTF8: true, ChildCType: "UTF-8"}
	environment := []string{"LANG=C", "LC_COLLATE=de_DE"}
	want := []string{"LANG=C", "LC_COLLATE=de_DE", "LC_CTYPE=UTF-8"}
	if got := state.ChildEnvironment(environment); !reflect.DeepEqual(got, want) {
		t.Fatalf("ChildEnvironment() = %v, want %v", got, want)
	}
}

func TestChildEnvironmentLeavesUnsupportedLocaleAlone(t *testing.T) {
	environment := []string{"LC_ALL=C"}
	if got := (State{}).ChildEnvironment(environment); !reflect.DeepEqual(got, environment) {
		t.Fatalf("ChildEnvironment() = %v, want %v", got, environment)
	}
}

func TestForceUTF8ChildEnvironmentDoesNotInitializeForCustomEnvironment(t *testing.T) {
	stateMu.RLock()
	initialized := ready
	stateMu.RUnlock()
	if initialized {
		t.Skip("process locale was initialized by an earlier test")
	}

	_ = ForceUTF8ChildEnvironment([]string{"LC_ALL=C", "LANG=C"})
	stateMu.RLock()
	initialized = ready
	stateMu.RUnlock()
	if initialized {
		t.Fatal("a custom child environment initialized the process locale")
	}
}
