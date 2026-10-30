//go:build !gui || guitest || (!windows && !darwin && !linux) || (linux && !cgo)

package main

func showUIStartupError(error) {}
