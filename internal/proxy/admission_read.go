package proxy

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

const admissionReadSize = 8 << 10

type admissionReadResult struct {
	data []byte
	err  error
}

// admissionReader owns local reads from acceptance through bridging. One queued
// chunk and one in-flight read bound preread storage to 16 KiB; the bridge can
// retain one additional chunk. Full staging applies TCP backpressure until dial
// completes. These copies fit the existing 256 KiB per-client reservation.
// Terminal errors cancel further reads while a valid FIN remains deliverable.
type admissionReader struct {
	conn         net.Conn
	results      chan admissionReadResult
	stop, done   chan struct{}
	pending      admissionReadResult // bridge reader only
	watchCancel  context.CancelFunc
	watchDone    chan struct{}
	failureMu    sync.Mutex // protects the first preread/socket failure
	firstFailure error
}

// socketErrorSource inspects terminal state without reading or discarding bytes.
// TCP sockets and deterministic socket-edge test doubles implement this seam.
type socketErrorSource interface {
	socketError() error
}

func (r *admissionReader) watchErrors(source socketErrorSource, cancel context.CancelFunc) {
	ctx, stop := context.WithCancel(context.Background())
	r.watchCancel = stop
	r.watchDone = make(chan struct{})
	go func() {
		defer close(r.watchDone)
		// One bounded watcher per admission; no readiness loop over unread data
		// or FIN, which could spin forever while staging is full.
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err := source.socketError(); err != nil {
				r.setFailure(err)
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// stopWatching joins before handoff: SO_ERROR consumes the pending error, so
// the watcher must never steal an error from the admitted bridge's byte-reader.
func (r *admissionReader) stopWatching() {
	if r.watchCancel != nil {
		r.watchCancel()
		<-r.watchDone
	}
}

func preread(conn net.Conn, cancel context.CancelFunc) *admissionReader {
	r := &admissionReader{conn: conn, results: make(chan admissionReadResult, 1), stop: make(chan struct{}), done: make(chan struct{})}
	if source, err := admissionSocketErrors(conn); err != nil {
		r.setFailure(err)
		cancel()
	} else if source != nil {
		r.watchErrors(source, cancel)
	}
	go func() {
		defer close(r.done)
		defer close(r.results)
		for {
			select {
			case <-r.stop:
				return
			default:
			}
			data := make([]byte, admissionReadSize)
			n, err := conn.Read(data)
			if err != nil && err != io.EOF {
				r.setFailure(err)
				cancel()
			}
			select {
			case r.results <- admissionReadResult{data[:n], err}:
			case <-r.stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return r
}

func (r *admissionReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending.data) == 0 && r.pending.err == nil {
		next, ok := <-r.results
		if !ok {
			return 0, io.EOF
		}
		r.pending = next
	}
	n := copy(p, r.pending.data)
	r.pending.data = r.pending.data[n:]
	if len(r.pending.data) != 0 {
		return n, nil
	}
	return n, r.pending.err
}

// close joins the socket watcher and a blocked read or staging send. Serve calls
// it exactly once, after the bridge (if any) has returned.
func (r *admissionReader) close() {
	r.stopWatching()
	close(r.stop)
	r.conn.Close()
	<-r.done
}

func (r *admissionReader) setFailure(err error) {
	r.failureMu.Lock()
	defer r.failureMu.Unlock()
	if r.firstFailure == nil {
		r.firstFailure = err
	}
}
func (r *admissionReader) failure() error {
	r.failureMu.Lock()
	defer r.failureMu.Unlock()
	return r.firstFailure
}
