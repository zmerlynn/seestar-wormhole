package browse

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// reported is one outcome a Reporter got.
type reported struct {
	op       string
	reason   string // the error's classified reason
	canceled bool
	seq      uint64
}

func (r reported) String() string {
	return fmt.Sprintf("%s/%s/%v/%d", r.op, r.reason, r.canceled, r.seq)
}

// reports records the outcomes of a Reporter, numbered from 1.
type reports struct {
	mu   sync.Mutex
	next uint64
	got  []reported
}

func (r *reports) reporter() Reporter {
	return Reporter{
		Report: func(op string, err error, canceled bool, seq uint64) {
			_, reason := diagnostic.OpFields(op, err)
			r.mu.Lock()
			defer r.mu.Unlock()
			r.got = append(r.got, reported{op, reason, canceled, seq})
		},
		Seq: func() uint64 {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.next++
			return r.next
		},
	}
}

func (r *reports) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, g := range r.got {
		out = append(out, g.String())
	}
	return out
}

func wantReports(t *testing.T, r *reports, want ...string) {
	t.Helper()
	if got := r.list(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("reported %q, want %q", got, want)
	}
}

// A listing answered reports a read and a records success under one
// number; a file served reports a read.
func TestReportsSuccess(t *testing.T) {
	r := &reports{}
	e := newEnv(t, envOptions{dest: true, reports: r})
	if w := e.serve("GET", "/api/list?path=MyWorks"); w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w := e.serve("GET", "/file?path=MyWorks%2FM%2031%2FX.jpg"); w.Code != 200 {
		t.Fatalf("file: %d %s", w.Code, w.Body)
	}
	wantReports(t, r, "connect/none/false/1", "read/none/false/2", "records/none/false/2", "read/none/false/3")
}

// A failure reports its op and classified reason, and its log line carries
// the source, op, phase and reason before the error. A missing path, and a
// request whose session couldn't be had, report no read failure.
func TestReportsFailure(t *testing.T) {
	t.Run("records", func(t *testing.T) {
		r := &reports{}
		e := newEnv(t, envOptions{dest: true, recordsErr: errors.New("PATH-SENTINEL unreadable"), reports: r})
		e.serve("GET", "/api/list?path=MyWorks")
		wantReports(t, r, "records/operation_failed/false/1")
		if !strings.Contains(e.logs.String(), `msg="request failed" source=browse op=records phase=records reason=operation_failed path=MyWorks error="PATH-SENTINEL unreadable"`) {
			t.Errorf("logs:\n%s", e.logs)
		}
	})
	t.Run("read", func(t *testing.T) {
		r := &reports{}
		e := newEnv(t, envOptions{reports: r, setup: func(s *spy) {
			s.fake.Faults = map[string]remote.Fault{"MyWorks/M 31/X.jpg": {OpenErr: context.DeadlineExceeded}}
		}})
		e.serve("GET", "/file?path=MyWorks%2FM%2031%2FX.jpg")
		e.serve("GET", "/file?path=MyWorks%2Fnone.jpg")
		// The timeout retires the session, so the next request dials again.
		wantReports(t, r, "connect/none/false/1", "read/timeout/false/2", "connect/none/false/3")
		if !strings.Contains(e.logs.String(), `msg="request failed" source=browse op=read phase=read reason=timeout path="MyWorks/M 31/X.jpg"`) {
			t.Errorf("logs:\n%s", e.logs)
		}
	})
	t.Run("connect", func(t *testing.T) {
		r := &reports{}
		e := newEnv(t, envOptions{dialErr: errors.New("HOST-SENTINEL refused"), reports: r})
		e.serve("GET", "/api/list?path=MyWorks")
		wantReports(t, r, "connect/operation_failed/false/1")
		logs := e.logs.String()
		if !strings.Contains(logs, `msg="connect failed" source=browse op=connect phase=connect reason=operation_failed error="HOST-SENTINEL refused"`) ||
			!strings.Contains(logs, `msg="request failed" source=browse op=read phase=connect reason=operation_failed`) {
			t.Errorf("logs:\n%s", logs)
		}
	})
}

// A dial with no answer reads as before and classifies as a timeout.
func TestReportsDialTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &reports{}
		d := &dialer{fsys: fstest.MapFS{}}
		d.setup = func(*spy) { time.Sleep(dialTimeout + 5*time.Second) }
		log, _ := newLog()
		p := NewSessions(context.Background(), d.dial, log, r.reporter())
		defer p.Close()
		_, err := p.Get(context.Background())
		if want := "connect to the device: no answer after 30s"; err == nil || err.Error() != want {
			t.Fatalf("Get = %v, want %q", err, want)
		}
		synctest.Wait()
		wantReports(t, r, "connect/timeout/false/1")
	})
}

// A dial that ends as the pool closes reports nothing, and a request gone
// away before its listing is answered reports nothing.
func TestReportsNothingCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &reports{}
		d := &dialer{fsys: fstest.MapFS{}, gate: make(chan struct{})}
		log, _ := newLog()
		p := NewSessions(context.Background(), d.dial, log, r.reporter())
		go p.Get(context.Background())
		synctest.Wait()
		p.Close()
		close(d.gate)
		synctest.Wait()
		wantReports(t, r)
	})
	r := &reports{}
	e := newEnv(t, envOptions{dest: true, reports: r})
	e.serve("GET", "/api/list?path=MyWorks")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, "GET", "/api/list?path=MyWorks", nil)
	req.Host = e.addr.String()
	e.h.ServeHTTP(httptest.NewRecorder(), req)
	wantReports(t, r, "connect/none/false/1", "read/none/false/2", "records/none/false/2")
}
