// Package guard keeps a panic in a copier goroutine from ending the process.
//
// The copier shares a process with the proxy, and an unrecovered panic in
// any goroutine ends every goroutine at once, the proxy's included. net/http
// already recovers a handler's panic, but not one in a goroutine it starts,
// such as a multipart response's reads. Each goroutine the copier starts,
// each timer and context callback it arms, and each call it makes from a
// goroutine it doesn't own recovers its own with [Run] or [Recover], logs
// where it was recovered, and carries on as if the work had failed.
//
// The record names the recovery site, the panic value and the stack. The
// copier holds no account credentials; the proxy's and the cloud client's
// own recovery points, which can, still log the site alone.
package guard

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Run calls f and returns its error. If f panics, Run logs where the panic
// was recovered, the panic value and the stack to log (a nil log means
// slog.Default()) and returns an error naming where, in place of f's.
func Run(log *slog.Logger, where string, f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = report(log, where, r, debug.Stack())
		}
	}()
	return f()
}

// Recover, deferred at the start of a goroutine, recovers a panic in it and
// logs it as [Run] does. A goroutine that must still clean up after a panic
// does that work in its own deferred calls, deferred before Recover.
func Recover(log *slog.Logger, where string) {
	if r := recover(); r != nil {
		report(log, where, r, debug.Stack())
	}
}

// Panic is a panic [Catch] recovered, with the stack where it was raised.
type Panic struct {
	Value any
	Stack []byte
}

// Catch calls f and returns the panic it recovered from f, or nil. It logs
// nothing, so a callback can publish that it is done before it logs the
// panic with [Panic.Report].
func Catch(f func()) (p *Panic) {
	defer func() {
		if r := recover(); r != nil {
			p = &Panic{Value: r, Stack: debug.Stack()}
		}
	}()
	f()
	return nil
}

// Report logs p as [Run] does, and returns the same error. A nil p logs
// nothing and returns nil.
func (p *Panic) Report(log *slog.Logger, where string) error {
	if p == nil {
		return nil
	}
	return report(log, where, p.Value, p.Stack)
}

// report logs the recovery site, the panic value and the stack, each clipped
// to keep the record within a log sink's limit. The returned
// error names only where, since callers show it as a failure's title.
func report(log *slog.Logger, where string, value any, stack []byte) error {
	if log == nil {
		log = slog.Default()
	}
	log.Error("internal error recovered", "where", where, "panic", diagnostic.Clip(fmt.Sprint(value), diagnostic.MaxValue),
		"stack", diagnostic.Clip(string(stack), diagnostic.MaxStack))
	return fmt.Errorf("internal error while %s", where)
}
