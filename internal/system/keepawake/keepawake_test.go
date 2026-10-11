package keepawake

import (
	"reflect"
	"testing"
)

func TestRunStartsAndStopsInhibitorAroundTurn(t *testing.T) {
	var events []string
	Run(true, func() func() {
		events = append(events, "start")
		return func() { events = append(events, "stop") }
	}, func() { events = append(events, "turn") })
	if want := []string{"start", "turn", "stop"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestRunSkipsInhibitorWhenDisabled(t *testing.T) {
	var events []string
	Run(false, func() func() {
		events = append(events, "start")
		return func() { events = append(events, "stop") }
	}, func() { events = append(events, "turn") })
	if want := []string{"turn"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestRunStillRunsWhenInhibitorCannotStart(t *testing.T) {
	called := false
	Run(true, func() func() { return nil }, func() { called = true })
	if !called {
		t.Fatal("turn did not run without an available inhibitor")
	}
}
