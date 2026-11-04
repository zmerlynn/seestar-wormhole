package state

import "errors"

// ErrLocked reports that another open State holds the destination's lock.
var ErrLocked = errors.New("another sync is using this destination")
