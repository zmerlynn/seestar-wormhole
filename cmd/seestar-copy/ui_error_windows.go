//go:build gui && !guitest

package main

func showStartupError(err error) { messageBox(startupErrorTitle, err.Error()) }
