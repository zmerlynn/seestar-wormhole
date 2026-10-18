package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
	"github.com/rclone/rclone/fs"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// pullConnections owns every SMB transport, including partially constructed
// backends. mu protects registration only; no lock spans socket I/O. Shutdown
// deadlines bound SMB Echo/Logoff, whose upstream methods ignore Shutdown's ctx.
type pullConnections struct {
	logger      *slog.Logger
	dialer      proxy.Dialer
	workContext context.Context
	mu          sync.Mutex
	connections map[*pullConn]struct{}
	closing     bool
}
type pullConn struct {
	net.Conn
	owner       *pullConnections
	stopCancel  func() bool
	workContext context.Context
	canceled    atomic.Bool
}

func (c *pullConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	return n, c.workError(err)
}
func (c *pullConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	return n, c.workError(err)
}
func (c *pullConn) workError(err error) error {
	// The direct session's own shutdown publishes ErrClosed, not net.ErrClosed.
	// It may win the race with our socket watcher. Preserve the owning work
	// cause for that exact leaf; independent session failure remains in Close.
	if err == session.ErrClosed && c.workContext != nil && c.workContext.Err() != nil {
		return c.workContext.Err()
	}
	// A transport closed by its work watcher reports that context's cause,
	// including teardown I/O racing between SMB's echo and logoff. Independent
	// closes and other I/O failures retain their original errors.
	if c.canceled.Load() {
		leaf := err
		for {
			op, ok := leaf.(*net.OpError)
			if !ok {
				break
			}
			leaf = op.Err
		}
		// Do not unwrap typed policy errors or collapse joined I/O siblings.
		switch leaf.(type) {
		case interface{ Unwrap() []error }, interface{ Unwrap() error }:
			return err
		}
		if errors.Is(leaf, net.ErrClosed) {
			return c.workContext.Err()
		}
	}
	return err
}

func (c *pullConn) Close() error {
	if c.stopCancel != nil {
		c.stopCancel()
	}
	c.owner.mu.Lock()
	delete(c.owner.connections, c)
	c.owner.mu.Unlock()
	return c.Conn.Close()
}
func (p *pullConnections) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	raw, err := p.dialer.DialPort(ctx, 445)
	if err != nil {
		return nil, err
	}
	// SMB mount/pool operations do not all inherit the work context. Bound
	// the socket as well, and close on cancellation, including during setup.
	transportContext := p.workContext
	if transportContext == nil {
		transportContext = ctx
	}
	if deadline, ok := transportContext.Deadline(); ok {
		if err := raw.SetDeadline(deadline); err != nil {
			raw.Close()
			return nil, err
		}
	}
	c := &pullConn{Conn: raw, owner: p, workContext: transportContext}
	c.stopCancel = context.AfterFunc(transportContext, func() {
		c.canceled.Store(true)
		raw.Close()
	})
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		c.stopCancel()
		raw.Close()
		return nil, net.ErrClosed
	}
	if p.connections == nil {
		p.connections = map[*pullConn]struct{}{}
	}
	p.connections[c] = struct{}{}
	p.mu.Unlock()
	return c, nil
}
func (p *pullConnections) detach() []*pullConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closing = true
	connections := make([]*pullConn, 0, len(p.connections))
	for c := range p.connections {
		connections = append(connections, c)
	}
	return connections
}
func (p *pullConnections) close() {
	for _, c := range p.detach() {
		c.Close()
	}
}
func (p *pullConnections) shutdown(source fs.Fs) error { return p.shutdownSMB(source, false) }

func (p *pullConnections) shutdownAfterWork(source fs.Fs) error { return p.shutdownSMB(source, true) }

func (p *pullConnections) shutdownSMB(source fs.Fs, completed bool) error {
	return pull.Operation(p.logger, func() error { return p.shutdownOperation(source, completed) })
}

func (p *pullConnections) shutdownOperation(source fs.Fs, completed bool) error {
	connections := p.detach()
	defer func() {
		for _, c := range connections {
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	for _, c := range connections {
		deadlineErr := c.SetDeadline(time.Now().Add(5 * time.Second))
		// The work watcher may already have closed this socket. That expected
		// close adds no failure, but a live socket or other error still does.
		if errors.Is(deadlineErr, net.ErrClosed) && onlySMBPeerClosure(deadlineErr) && (completed || p.workContext != nil && p.workContext.Err() != nil) {
			deadlineErr = nil
		}
		err = errors.Join(err, deadlineErr)
	}
	if source.Features().Shutdown != nil {
		backendErr := source.Features().Shutdown(ctx)
		if completed {
			backendErr = completedSMBShutdownError(backendErr)
		}
		err = errors.Join(err, backendErr)
	}
	return err
}

// A completed, verified batch has no SMB work left. A peer that closes between
// Echo and unmount/logoff has already accomplished this pool-only cleanup. Only
// typed transport closures are redundant; other Shutdown errors still fail.
func completedSMBShutdownError(err error) error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var remaining error
		for _, cause := range joined.Unwrap() {
			remaining = errors.Join(remaining, completedSMBShutdownError(cause))
		}
		return remaining
	}
	if transport, ok := err.(*smb2.TransportError); ok && onlySMBPeerClosure(transport.Err) {
		return nil
	}
	return err
}
func onlySMBPeerClosure(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlySMBPeerClosure(cause) {
				return false
			}
		}
		return true
	}
	if op, ok := err.(*net.OpError); ok {
		return onlySMBPeerClosure(op.Err)
	}
	// A policy wrapper, a cancellation or a timeout must not become a clean close.
	if _, ok := err.(interface{ Unwrap() error }); ok {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || err == session.ErrClosed
}
