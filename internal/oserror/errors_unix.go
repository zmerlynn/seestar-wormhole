//go:build !windows

// Package oserror matches portable errno labels against native OS errors.
package oserror

import "errors"

// Is retains Unix errors.Is behavior.
func Is(err, target error) bool { return errors.Is(err, target) }
