// Package filelock takes exclusive OS locks on open files.
//
// A lock belongs to the open file, not the process: flock on Unix, and a
// LockFileEx lock on the first byte on Windows. A second open of the same
// file conflicts even in the same process. A lock lasts until [Unlock], until
// the file is closed, or until the process exits.
package filelock

import (
	"errors"
	"os"
	"time"
)

// ErrTimeout reports that [Lock] gave up waiting for the lock.
var ErrTimeout = errors.New("file lock: timed out")

// TryLock takes the exclusive lock on f without waiting. It reports false,
// with no error, when another open file holds the lock.
func TryLock(f *os.File) (bool, error) {
	return lock(f, false)
}

// Lock takes the exclusive lock on f. With a positive timeout it retries
// every 10 ms and returns [ErrTimeout] once the timeout has passed;
// otherwise it blocks until the lock is free. Windows can block only on a
// file opened for synchronous I/O, as os and privatefile open files.
func Lock(f *os.File, timeout time.Duration) error {
	if timeout <= 0 {
		_, err := lock(f, true)
		return err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		locked, err := lock(f, false)
		if locked || err != nil {
			return err
		}
		select {
		case <-deadline.C:
			return ErrTimeout
		case <-tick.C:
		}
	}
}

// Unlock releases the lock on f.
func Unlock(f *os.File) error {
	return unlock(f)
}
