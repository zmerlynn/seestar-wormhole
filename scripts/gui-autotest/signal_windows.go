//go:build guitest && windows

package main

import (
	"errors"
	"os"
)

func terminate(*os.Process, bool) error {
	return errors.New("GUI subsystem console signal is not exercised")
}
