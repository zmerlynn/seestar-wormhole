//go:build !gui || guitest || (!windows && !darwin && !linux) || (linux && !cgo)

package main

// showStartupError has nothing to show without a window, and a guitest
// build shows nothing so that CI never waits on a dialog: the error is on
// stderr.
func showStartupError(error) {}
