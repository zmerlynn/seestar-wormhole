package browse

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"
)

// started is a request running in its own goroutine.
type started struct {
	cancel context.CancelFunc
	done   chan *httptest.ResponseRecorder
}

func (e *env) start(target string) started {
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequestWithContext(ctx, "GET", target, nil)
	r.Host = e.addr.String()
	s := started{cancel: cancel, done: make(chan *httptest.ResponseRecorder, 1)}
	go func() {
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		s.done <- w
	}()
	return s
}

func (s started) returned() bool { return len(s.done) == 1 }

// firstOnly applies f to the first dialed spy only.
func firstOnly(f func(*spy)) func(*spy) {
	first := true
	return func(s *spy) {
		if first {
			f(s)
		}
		first = false
	}
}

// A canceled request whose SMB call never returns holds its session for the
// grace, then retires it, and the next request dials a new one.
func TestCanceledStuckCall(t *testing.T) {
	for block, target := range map[string]string{"Open": fitURL, "ReadDir": "/api/list?path=MyWorks"} {
		synctest.Test(t, func(t *testing.T) {
			e := newEnv(t, envOptions{setup: firstOnly(func(s *spy) { s.block = block })})
			req := e.start(target)
			other := e.start(target)
			synctest.Wait()
			req.cancel()
			time.Sleep(cancelGrace - time.Second)
			synctest.Wait()
			if req.returned() || e.d.session(0).closed() {
				t.Fatalf("%s: returned %v, closed %v before the grace ended", block, req.returned(), e.d.session(0).closed())
			}
			time.Sleep(time.Second)
			synctest.Wait()
			if !req.returned() || !e.d.session(0).closed() || e.logs.count("session closed", "reason=stuck") != 1 {
				t.Fatalf("%s: returned %v, closed %v at the grace; logs:\n%s", block, req.returned(), e.d.session(0).closed(), e.logs)
			}
			if w := <-other.done; w.Code != http.StatusBadGateway {
				t.Errorf("%s: the other request on the retired session = %d, want 502", block, w.Code)
			}
			if e.p.leases() != -1 {
				t.Errorf("%s: a current session is left", block)
			}
			if w := e.serve("GET", target); w.Code != 200 || e.d.dials() != 2 {
				t.Errorf("%s: next request = %d after %d dials", block, w.Code, e.d.dials())
			}
		})
	}
}

// A canceled request whose call returns within the grace keeps the session
// and closes its file normally.
func TestCanceledCallReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, envOptions{setup: func(s *spy) { s.openDelay = time.Second }})
		req := e.start(fitURL)
		synctest.Wait()
		req.cancel()
		time.Sleep(time.Second)
		synctest.Wait()
		if !req.returned() {
			t.Fatal("the request did not return once its call did")
		}
		s := e.d.session(0)
		if s.closed() || s.n("FileClose") != 1 || s.closeCtx[0] != nil {
			t.Fatalf("closed %v, %d file closes, file context %v; want kept, 1, live", s.closed(), s.n("FileClose"), s.closeCtx)
		}
		if s.n("Read") != 0 {
			t.Errorf("%d reads for a departed client", s.n("Read"))
		}
		time.Sleep(idleTimeout - time.Nanosecond)
		synctest.Wait()
		if s.closed() {
			t.Fatal("closed before the idle time")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if !s.closed() || e.logs.count("request failed") != 0 {
			t.Fatalf("closed %v; logs:\n%s", s.closed(), e.logs)
		}
	})
}

// A file Close that never returns keeps the lease until the grace retires
// the session; the handler releases only after Close returns.
func TestStuckFileClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, envOptions{setup: firstOnly(func(s *spy) { s.block = "FileClose" })})
		req := e.start("/file?path=MyWorks%2FM%2031%2FX.jpg")
		synctest.Wait()
		s := e.d.session(0)
		if s.n("FileClose") != 1 || e.p.leases() != 1 {
			t.Fatalf("%d file closes, %d leases; want 1 and 1", s.n("FileClose"), e.p.leases())
		}
		req.cancel()
		time.Sleep(cancelGrace - time.Second)
		synctest.Wait()
		if req.returned() || e.p.leases() != 1 {
			t.Fatalf("released before the grace: returned %v, %d leases", req.returned(), e.p.leases())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !req.returned() || !s.closed() || e.p.leases() != -1 {
			t.Fatalf("at the grace: returned %v, closed %v, %d leases", req.returned(), s.closed(), e.p.leases())
		}
	})
}

// cancelWriter cancels the request at its first body write.
type cancelWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w cancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return w.ResponseRecorder.Write(p)
}

// A client that leaves mid-download gets no further device read; the file
// is closed and the lease released.
func TestDepartedDownload(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), (maxBuffer*2+maxBuffer/2)/16)
	e := newEnv(t, envOptions{extra: fstest.MapFS{"big.fit": {Data: big, ModTime: t0}}})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequestWithContext(ctx, "GET", "/file?path=big.fit", nil)
	r.Host = e.addr.String()
	w := cancelWriter{httptest.NewRecorder(), cancel}
	e.h.ServeHTTP(w, r)
	s := e.d.session(0)
	if s.n("Read") != 1 || w.Body.Len() != maxBuffer {
		t.Errorf("%d device reads and %d bytes sent after the client left; want 1 and one buffer", s.n("Read"), w.Body.Len())
	}
	if s.n("FileClose") != 1 || e.p.leases() != 0 || s.closed() {
		t.Errorf("%d file closes, %d leases, closed %v; want 1, 0, kept", s.n("FileClose"), e.p.leases(), s.closed())
	}
	if e.logs.count("request failed") != 0 {
		t.Errorf("logged a departed client:\n%s", e.logs)
	}
}

// lateWriter fails its first body write once a device Read has started.
type lateWriter struct {
	*httptest.ResponseRecorder
	reading chan struct{}
}

func (w lateWriter) Write(p []byte) (int, error) {
	<-w.reading
	return 0, errors.New("client gone")
}

// startMultipart starts a two-range request whose writer fails on the first
// part's header, after http.ServeContent's producer has begun a device Read
// that read decides the outcome of.
func startMultipart(t *testing.T, read func(s *spy) error) (*env, started) {
	reading := make(chan struct{})
	e := newEnv(t, envOptions{setup: firstOnly(func(s *spy) {
		s.readFault = func() error {
			close(reading)
			return read(s)
		}
	})})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequestWithContext(ctx, "GET", fitURL, nil)
	r.Host = e.addr.String()
	r.Header.Set("Range", "bytes=0-9,20-29")
	req := started{cancel: cancel, done: make(chan *httptest.ResponseRecorder, 1)}
	go func() {
		w := lateWriter{httptest.NewRecorder(), reading}
		e.h.ServeHTTP(w, r)
		req.done <- w.ResponseRecorder
	}()
	return e, req
}

// A multipart response whose client fails mid-part waits for the device
// Read in progress before it closes the file or releases the lease, so the
// grace still retires a Read that never returns.
func TestMultipartStuckRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, req := startMultipart(t, func(s *spy) error {
			<-s.done
			return fs.ErrClosed
		})
		synctest.Wait()
		s := e.d.session(0)
		if req.returned() || s.n("FileClose") != 0 || e.p.leases() != 1 {
			t.Fatalf("during the Read: returned %v, %d file closes, %d leases; want false, 0, 1", req.returned(), s.n("FileClose"), e.p.leases())
		}
		req.cancel()
		time.Sleep(cancelGrace - time.Second)
		synctest.Wait()
		if req.returned() || s.n("FileClose") != 0 || e.p.leases() != 1 {
			t.Fatalf("before the grace: returned %v, %d file closes, %d leases", req.returned(), s.n("FileClose"), e.p.leases())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !req.returned() || !s.closed() || s.n("FileClose") != 1 || e.p.leases() != -1 || e.logs.count("session closed", "reason=stuck") != 1 {
			t.Fatalf("at the grace: returned %v, closed %v, %d file closes, %d leases; logs:\n%s",
				req.returned(), s.closed(), s.n("FileClose"), e.p.leases(), e.logs)
		}
		if s.n("Read") != 1 || e.logs.count("request failed") != 0 {
			t.Errorf("%d reads; logs:\n%s", s.n("Read"), e.logs)
		}
	})
}

// A Read in progress when a multipart client fails is waited for; its late
// device error is logged and retires the session.
func TestMultipartLateReadError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, req := startMultipart(t, func(*spy) error {
			time.Sleep(time.Second)
			return errors.New("read fault")
		})
		synctest.Wait()
		s := e.d.session(0)
		if req.returned() || s.n("FileClose") != 0 || e.p.leases() != 1 {
			t.Fatalf("during the Read: returned %v, %d file closes, %d leases; want false, 0, 1", req.returned(), s.n("FileClose"), e.p.leases())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !req.returned() || !s.closed() || s.n("FileClose") != 1 || s.n("Read") != 1 {
			t.Fatalf("after the Read: returned %v, closed %v, %d file closes, %d reads", req.returned(), s.closed(), s.n("FileClose"), s.n("Read"))
		}
		if e.logs.count("request failed", "read fault") != 1 || e.logs.count("session closed", "reason=failed") != 1 {
			t.Errorf("logs:\n%s", e.logs)
		}
	})
}
