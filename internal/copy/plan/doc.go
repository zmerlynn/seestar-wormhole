// Package plan decides what one sync pass does with each regular file in the
// device listing: wait for it, leave it, copy it, replace it, adopt the local
// copy, or fail it. It also makes delete's decisions that need only the
// listing and the state: which files may be deleted, and which folders the
// deletions emptied.
//
// It is pure: it does no file or network I/O, reads no clock, starts no
// goroutines and keeps no package state. Everything a decision depends on,
// including the current time and the target operating system, is an input,
// so the rules are tested for every platform on any platform.
package plan
