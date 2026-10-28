//go:build guitest && !windows

package main

import (
	"os"
	"syscall"
)

func terminate(p *os.Process, interrupt bool) error {
	if interrupt {
		return p.Signal(os.Interrupt)
	}
	return p.Signal(syscall.SIGTERM)
}
