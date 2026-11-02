// Package readonly is a process-wide guard for a dry run. Once [Enable] is
// called, [Check] panics, and every function that changes the local
// filesystem calls Check first. So a dry run that reached a write by mistake
// stops before it changes anything, rather than going on quietly.
//
// A test in this package checks that every function under internal/ and
// cmd/ that creates, writes, renames, removes or otherwise changes a file or
// folder starts with a call to Check.
package readonly

import "sync/atomic"

var enabled atomic.Bool

// Enable turns the guard on for the rest of the process.
func Enable() { enabled.Store(true) }

// Check panics if the guard is on. op names the operation, for the panic
// message.
func Check(op string) {
	if enabled.Load() {
		panic("readonly: " + op + " would change the local filesystem during a dry run")
	}
}

// Reset turns the guard off. It is for tests only, which run dry runs and
// real passes in one process; the coverage test refuses a call to it from
// any other file.
func Reset() { enabled.Store(false) }
