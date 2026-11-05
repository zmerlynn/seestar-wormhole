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
// The record names only the recovery site: like the proxy's own recovery
// points, it carries neither the panic value nor the stack, which can hold
// device paths, addresses or credentials the log must not.
package guard

import (
	"fmt"
	"log/slog"
)

// Run calls f and returns its error. If f panics, Run logs where the panic
// was recovered to log (a nil log means slog.Default()) and returns an error
// naming where, in place of f's. Neither holds the panic value.
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

// Panic is a panic [Catch] recovered.
type Panic struct {
	Value any
}

// Catch calls f and returns the panic it recovered from f, or nil. It logs
// nothing, so a callback can publish that it is done before it logs the
// panic with [Panic.Report].
func Catch(f func()) (p *Panic) {
	defer func() {
		if r := recover(); r != nil {
			p = &Panic{Value: r}
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
	return report(log, where, p.Value)
}

// report logs the recovery site alone. The returned error names only where
// too, since callers log it and show it.
func report(log *slog.Logger, where string, _ any) error {
	if log == nil {
		log = slog.Default()
	}
	log.Error("internal error recovered", "where", where)
	return fmt.Errorf("internal error while %s", where)
}
