//go:build guitest

package main

import "testing"

func TestRelaunchScenario(t *testing.T) {
	for scenario, want := range map[string]string{
		"fresh": "relaunch", "relaunch": "relaunch", "errors": "relaunch",
		"dark": "dark-relaunch", "dark-relaunch": "dark-relaunch",
	} {
		if got := relaunchScenario(scenario); got != want {
			t.Errorf("%s: got %s, want %s", scenario, got, want)
		}
	}
}
