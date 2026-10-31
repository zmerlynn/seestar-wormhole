//go:build !windows || !gui

package main

import "testing"

func TestConsoleControlCancellation(t *testing.T) {
	t.Skip("requires native Windows and the gui build tag")
}
