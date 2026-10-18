package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rclone/rclone/fs"
)

type connectionDialFixture struct{ net.Conn }

func (f connectionDialFixture) DialPort(context.Context, uint16) (net.Conn, error) {
	return f.Conn, nil
}

type stalledShutdownFs struct {
	fs.Fs
	conn net.Conn
}

func (f stalledShutdownFs) Features() *fs.Features {
	return &fs.Features{Shutdown: func(context.Context) error {
		_, err := f.conn.Read(make([]byte, 1))
		return err // deliberately ignores ctx, like upstream Echo
	}}
}

func TestPullSMBShutdownIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw, peer := net.Pipe()
		defer peer.Close()
		connections := &pullConnections{dialer: connectionDialFixture{raw}}
		conn, err := connections.dial(context.Background(), "tcp", "ignored")
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		err = connections.shutdown(stalledShutdownFs{conn: conn})
		if err == nil || time.Since(start) != 5*time.Second {
			t.Fatalf("shutdown elapsed=%v err=%v", time.Since(start), err)
		}
		if len(connections.connections) != 0 {
			t.Fatal("shutdown retained transports")
		}
	})
}

func TestPullSMBTransportUsesWorkLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw, peer := net.Pipe()
		defer peer.Close()
		work, cancelWork := context.WithTimeout(context.Background(), time.Minute)
		defer cancelWork()
		worker, cancelWorker := context.WithCancel(work)
		connections := &pullConnections{dialer: connectionDialFixture{raw}, workContext: work}
		defer connections.close()
		conn, err := connections.dial(worker, "tcp", "ignored")
		if err != nil {
			t.Fatal(err)
		}
		cancelWorker()
		synctest.Wait()
		sent := make(chan error, 1)
		go func() { _, err := peer.Write([]byte{42}); sent <- err }()
		data := make([]byte, 1)
		if _, err := conn.Read(data); err != nil || data[0] != 42 {
			t.Fatalf("worker completion closed pooled transport: %v", err)
		}
		<-sent
		cancelWork()
		synctest.Wait()
		if _, err := conn.Read(data); err == nil {
			t.Fatal("work cancellation left SMB socket open")
		}
	})
}

type deadlineFailureConn struct {
	net.Conn
	err error
}

func (c deadlineFailureConn) SetDeadline(time.Time) error { return c.err }

type shutdownFailureFs struct {
	fs.Fs
	err error
}

func (f shutdownFailureFs) Features() *fs.Features {
	return &fs.Features{Shutdown: func(context.Context) error { return f.err }}
}
func TestPullShutdownDeadlineErrorPrecedence(t *testing.T) {
	for _, test := range []struct {
		name              string
		done              bool
		deadline, backend error
		want              error
	}{
		{"canceled-closed", true, net.ErrClosed, nil, nil},
		{"live-closed", false, net.ErrClosed, nil, net.ErrClosed},
		{"canceled-other", true, errors.ErrUnsupported, nil, errors.ErrUnsupported},
		{"canceled-backend", true, net.ErrClosed, errors.ErrUnsupported, errors.ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			raw, peer := net.Pipe()
			defer peer.Close()
			p := &pullConnections{workContext: ctx, connections: map[*pullConn]struct{}{}}
			c := &pullConn{Conn: deadlineFailureConn{raw, test.deadline}, owner: p}
			p.connections[c] = struct{}{}
			if test.done {
				cancel()
			}
			err := p.shutdown(shutdownFailureFs{err: test.backend})
			if test.want == nil && err != nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("shutdown=%v want %v", err, test.want)
			}
		})
	}
}

// The work watcher, rather than an independent peer/backend failure, owns this
// closure. Both SMB writes during logoff and reads must retain that cause.
type closedIOConn struct {
	net.Conn
	err error
}

func (c closedIOConn) Read([]byte) (int, error)  { return 0, c.err }
func (c closedIOConn) Write([]byte) (int, error) { return 0, c.err }
func TestPullTransportRetainsCancellationCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, cancelWork := range []bool{false, true} {
			raw, peer := net.Pipe()
			defer peer.Close()
			ctx, cancel := context.WithCancel(t.Context())
			p := &pullConnections{dialer: connectionDialFixture{closedIOConn{raw, net.ErrClosed}}, workContext: ctx}
			conn, err := p.dial(ctx, "tcp", "ignored")
			if err != nil {
				t.Fatal(err)
			}
			if cancelWork {
				cancel()
				synctest.Wait()
			}
			_, readErr := conn.Read(make([]byte, 1))
			_, writeErr := conn.Write([]byte{1})
			want := error(net.ErrClosed)
			if cancelWork {
				want = context.Canceled
			}
			if !errors.Is(readErr, want) || !errors.Is(writeErr, want) {
				t.Fatalf("cancel=%t read=%v write=%v want=%v", cancelWork, readErr, writeErr, want)
			}
			conn.Close()
			cancel()
		}
	})
}

func TestPullCanceledTransportKeepsIndependentIOFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, cause := range []error{errors.ErrUnsupported, errors.Join(net.ErrClosed, errors.ErrUnsupported)} {
			raw, peer := net.Pipe()
			defer peer.Close()
			ctx, cancel := context.WithCancel(t.Context())
			p := &pullConnections{dialer: connectionDialFixture{closedIOConn{raw, cause}}, workContext: ctx}
			conn, err := p.dial(ctx, "tcp", "ignored")
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			synctest.Wait()
			_, readErr := conn.Read(make([]byte, 1))
			_, writeErr := conn.Write([]byte{1})
			if !errors.Is(readErr, errors.ErrUnsupported) || !errors.Is(writeErr, errors.ErrUnsupported) {
				t.Fatalf("lost I/O sibling: read=%v write=%v", readErr, writeErr)
			}
			conn.Close()
		}
	})
}
