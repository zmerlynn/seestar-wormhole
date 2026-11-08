package ui

// These are the composed tests: the file tools reach a real lazy
// proxy.Session, over proxytest's simulated cloud and device, through
// the run's attachment. They prove the session's demand and release
// behavior under the host's rules, in a synctest bubble with no sockets.

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy/proxytest"
)

// closingPeer answers each device port-445 stream by counting it and
// ending it once the first request arrives: reads then see the stream's
// end, so go-smb2's negotiate fails, and writes are still taken, so the
// device never resets the channel. The stream ends only when the host
// closes its side.
type closingPeer struct{ streams atomic.Int32 }

func (p *closingPeer) dial(context.Context) (net.Conn, error) {
	p.streams.Add(1)
	c, other := net.Pipe()
	other.Close()
	return &endingConn{Conn: c, asked: make(chan struct{})}, nil
}

// endingConn is a closingPeer stream.
type endingConn struct {
	net.Conn // for its addresses and deadlines; never read or written
	once     sync.Once
	asked    chan struct{} // closed once a request has arrived
}

func (c *endingConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.asked) })
	return len(b), nil
}

func (c *endingConn) Read([]byte) (int, error) {
	<-c.asked
	return 0, io.EOF
}

func (c *endingConn) Close() error {
	c.once.Do(func() { close(c.asked) })
	return c.Conn.Close()
}

// composedSession starts a lazy session with a one-second idle release
// over cl and a wire whose port 445 port445 answers.
func composedSession(t *testing.T, ctx context.Context, cl *proxytest.Cloud, status *proxy.Status, port445 func(context.Context) (net.Conn, error)) (*proxy.Session, error) {
	client, err := proxytest.NewClient(t, cloudhttp.Config{Transport: cl})
	if err != nil {
		return nil, err
	}
	return proxy.StartWithWire(ctx, proxy.Config{Idle: time.Second, Status: status}, client, proxytest.Device, proxytest.Verify, func(iotc.License) proxy.WireRunner {
		return proxytest.NewWire(t, proxytest.WireOptions{Port445: port445})
	})
}

// trackedChannels is how many channels the current run's attachment
// holds.
func trackedChannels(as *attachments) int {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.cur == nil {
		return 0
	}
	return len(as.cur.conns)
}

// A dormant session costs nothing until a file tool needs it: Browse's
// listing, through the real Mount, copyMountOptions' Dial and DialVia,
// opens the session once and one stream to port 445; the stream's end
// lets the session's idle release stop it, and the next listing opens
// it again.
func TestComposedMountDemandAndRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		realDial := dialVia
		e := newViaEnv(t, nil)
		dialVia = realDial
		cl := &proxytest.Cloud{}
		peer := &closingPeer{}
		attached := make(chan struct{})
		e.s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status, attach func(proxy.Dialer) (func(), bool)) error {
			s, err := composedSession(t, ctx, cl, status, peer.dial)
			if err != nil {
				return err
			}
			detach, ok := attach(s)
			if !ok {
				return s.Close()
			}
			status.MarkArmed()
			close(attached)
			<-ctx.Done()
			detach()
			return s.Close()
		}
		if code, body := e.start(testSettings()); code != 200 {
			t.Fatalf("Connect = %d %s", code, body)
		}
		<-attached
		defer e.stopRun()
		synctest.Wait()

		// 1. Dormant.
		tg, err := e.s.copyTarget()
		if err != nil || tg.Run == 0 || tg.Device != proxytest.Device.DisplayName() {
			t.Fatalf("target while dormant = %+v (%q, run %d), %v", tg, tg.Device, tg.Run, err)
		}
		if n := cl.Details.Load(); n != 0 {
			t.Fatalf("the dormant session asked the cloud %d times", n)
		}

		// 2. Demand.
		if code, body := e.browseList("MyWorks"); code == 200 || !strings.Contains(body, accountEndpoint) {
			t.Errorf("Browse at a peer that closes = %d %s", code, body)
		}
		synctest.Wait()
		if d, a, s := cl.Details.Load(), cl.Allocs.Load(), peer.streams.Load(); d != 1 || a != 1 || s != 1 {
			t.Errorf("the first listing: %d details, %d allocations, %d streams; want 1 each", d, a, s)
		}
		if n := trackedChannels(&e.s.controller.files); n != 0 {
			t.Errorf("%d channels still tracked", n)
		}

		// 3. Idle.
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if n := cl.Stops.Load(); n != 1 {
			t.Fatalf("the idle session stopped %d times, want 1", n)
		}

		// 4. Demand again.
		e.browseList("MyWorks")
		synctest.Wait()
		if a, s := cl.Allocs.Load(), peer.streams.Load(); a != 2 || s != 2 {
			t.Errorf("the second listing: %d allocations, %d streams; want 2 each", a, s)
		}
	})
}

// The attachment alone, dialing port 445 over a real lazy session,
// demands and releases it the same way.
func TestComposedAdapterDemandAndRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cl := &proxytest.Cloud{}
		peer := &closingPeer{}
		status := proxy.NewStatus()
		s, err := composedSession(t, t.Context(), cl, status, peer.dial)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		as := &attachments{}
		as.begin(t.Context(), 1)
		detach, ok := as.attach(1, s)
		if !ok {
			t.Fatal("attach refused")
		}
		defer detach()
		synctest.Wait()
		if n := cl.Details.Load(); n != 0 {
			t.Fatalf("the dormant session asked the cloud %d times", n)
		}
		open := func(round int) {
			t.Helper()
			c, err := as.dial(t.Context(), 1)
			if err != nil {
				t.Fatalf("dial %d: %v", round, err)
			}
			c.Write([]byte("request"))
			c.Read(make([]byte, 1)) // the peer closes after the request
			c.Close()
			synctest.Wait()
			if a, n := cl.Allocs.Load(), peer.streams.Load(); a != int32(round) || n != int32(round) {
				t.Errorf("dial %d: %d allocations, %d streams", round, a, n)
			}
			if n := trackedChannels(as); n != 0 {
				t.Errorf("dial %d: %d channels still tracked", round, n)
			}
		}
		open(1)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if n := cl.Stops.Load(); n != 1 {
			t.Fatalf("the idle session stopped %d times, want 1", n)
		}
		open(2)
	})
}

// stallFile is a download held at a barrier: its first read returns
// data, so the copier has a temporary file, and the next waits for
// barrier and then needs the session's channel.
type stallFile struct {
	remote.File
	s       *channelSession
	barrier <-chan struct{}
	reached chan<- struct{}
	read    bool
}

func (f *stallFile) wait() error {
	select {
	case f.reached <- struct{}{}:
	default:
	}
	<-f.barrier
	return f.s.ping(".")
}

func (f *stallFile) Read(b []byte) (int, error) {
	if f.read {
		if err := f.wait(); err != nil {
			return 0, err
		}
	}
	f.read = true
	return f.File.Read(b)
}

func (f *stallFile) WriteTo(w io.Writer) (int64, error) {
	buf := make([]byte, 16<<10)
	n, err := f.File.Read(buf)
	if n > 0 {
		if _, werr := w.Write(buf[:n]); werr != nil {
			return 0, werr
		}
	}
	if err != nil {
		return int64(n), err
	}
	if err := f.wait(); err != nil {
		return int64(n), err
	}
	rest, err := io.Copy(w, struct{ io.Reader }{f.File})
	return int64(n) + rest, err
}

// tempFiles lists the copier's temporary files under dir.
func tempFiles(dir string) []string {
	var found []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && plan.IsTempName(d.Name()) {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// A proxy restart doesn't wait for a pass of copying that holds the
// history lock: with a download held at a barrier over the composed
// session, Stop's detach returns; once the barrier opens, the file fails
// and leaves no temporary file; the pass's next look after a reconnect
// to the same telescope copies it over the new run; and the history
// stays locked until copying ends.
func TestCombinedRestartLockOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newViaEnv(t, nil)
		held := strings.Repeat("x", 256<<10)
		e.file("MyWorks/held.fit", held)
		// The bubble's clock starts in 2000: date the share's files
		// before it, or they look still written.
		old := time.Date(1999, 1, 2, 3, 4, 5, 0, time.UTC)
		for _, name := range []string{"MyWorks/a.jpg", "MyWorks/held.fit"} {
			if err := os.Chtimes(filepath.Join(e.shareDir, filepath.FromSlash(name)), old, old); err != nil {
				t.Fatal(err)
			}
		}
		barrier, reached := make(chan struct{}), make(chan struct{}, 1)
		e.wrap = func(s *channelSession, p string, f remote.File) remote.File {
			if p != "MyWorks/held.fit" {
				return f
			}
			return &stallFile{File: f, s: s, barrier: barrier, reached: reached}
		}
		cl, echo, log := &proxytest.Cloud{}, &echoDialer{}, &orderLog{}
		armed := make(chan struct{}, 2)
		e.s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status, attach func(proxy.Dialer) (func(), bool)) error {
			s, err := composedSession(t, ctx, cl, status, func(ctx context.Context) (net.Conn, error) { return echo.DialPort(ctx, 445) })
			if err != nil {
				return err
			}
			detach, ok := attach(s)
			if !ok {
				return s.Close()
			}
			status.MarkArmed()
			armed <- struct{}{}
			<-ctx.Done()
			detach()
			log.add("detached")
			return s.Close()
		}
		if code, body := e.start(testSettings()); code != 200 {
			t.Fatalf("Connect = %d %s", code, body)
		}
		<-armed
		e.settings()
		if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
			t.Fatalf("start copying = %d %s", code, body)
		}
		e.awaitChan(reached, "the download to reach the barrier")
		if len(tempFiles(e.folder)) == 0 {
			t.Fatal("the held download has no temporary file")
		}

		// Restart: Stop's detach returns while the barrier holds the pass.
		e.s.controller.stop()
		e.until("the run to end", func() bool { _, active := e.s.controller.snapshot(); return !active })
		if got := log.list(); len(got) != 1 {
			t.Errorf("events %q, want one detach", got)
		}
		if !e.s.copy.Busy() {
			t.Error("the held pass ended before the barrier opened")
		}
		close(barrier)
		e.until("the held file to fail", func() bool { state, _ := e.copying(); return state != "copying" })
		if temps := tempFiles(e.folder); len(temps) != 0 {
			t.Errorf("temporary files after the failure: %q", temps)
		}
		if got := e.copiedFile("MyWorks/held.fit"); got != "" {
			t.Errorf("the held file copied %d bytes over a closed run", len(got))
		}
		if st, err := state.Open(e.stateDir, e.folder, nil); !errors.Is(err, state.ErrLocked) {
			if st != nil {
				st.Close()
			}
			t.Errorf("history while copying is on: %v", err)
		}

		// Reattach to the same telescope; the next pass copies the file.
		if code, body := e.start(testSettings()); code != 200 {
			t.Fatalf("reconnect = %d %s", code, body)
		}
		<-armed
		e.until("the file over the new run", func() bool {
			e.post("/copy/api/copy", `{"action":"check"}`)
			return e.copiedFile("MyWorks/held.fit") == held
		})
		if st, err := state.Open(e.stateDir, e.folder, nil); !errors.Is(err, state.ErrLocked) {
			if st != nil {
				st.Close()
			}
			t.Errorf("history while copying is on after the reconnect: %v", err)
		}
		if code, body := e.post("/copy/api/copy", `{"action":"stop"}`); code != 200 {
			t.Fatalf("stop copying = %d %s", code, body)
		}
		e.until("copying to stop", func() bool { return !e.s.copy.Busy() })
		st, err := state.Open(e.stateDir, e.folder, nil)
		if err != nil {
			t.Fatalf("history once copying ended: %v", err)
		}
		st.Close()
		e.stopRun()
	})
}
