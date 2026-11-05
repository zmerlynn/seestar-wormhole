// Package guard keeps a panic in a copier goroutine from ending the process.
//
// The copier shares a process with the proxy, and an unrecovered panic in
// any goroutine ends every goroutine at once, the proxy's included. net/http
// already recovers a handler's panic, but not one in a goroutine it starts,
// such as a multipart response's reads. Each goroutine the copier starts,
// each timer and context callback it arms, and each call it makes from a
// goroutine it doesn't own recovers its own with [Run] or [Recover], logs
// it with its stack, and carries on as if the work had failed.
package guard

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Run calls f and returns its error. If f panics, Run logs the panic and
// its stack to log (a nil log means slog.Default()) and returns an error
// naming where, in place of f's.
func Run(log *slog.Logger, where string, f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = report(log, where, r)
		}
	}()
	return f()
}

// Recover, deferred at the start of a goroutine, recovers a panic in it and
// logs it as [Run] does. A goroutine that must still clean up after a panic
// does that work in its own deferred calls, deferred before Recover.
func Recover(log *slog.Logger, where string) {
	if r := recover(); r != nil {
		report(log, where, r)
	}
}

func report(log *slog.Logger, where string, r any) error {
	if log == nil {
		log = slog.Default()
	}
	log.Error("internal error recovered", "where", where, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	return fmt.Errorf("internal error while %s: %v", where, r)
}
