package ui

import "testing"

func TestQuitTerminalStatus(t *testing.T) {
	for _, native := range []string{"false", "true"} {
		t.Run(native, func(t *testing.T) {
			runPageRegression(t, "quit-status-test.js", "checkQuitStatus("+native+")")
		})
	}
}
