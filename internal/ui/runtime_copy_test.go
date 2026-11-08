package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// runtimeEnv runs runUIPresented with saved copy settings that start
// copying when the app opens, a fake telescope behind copyDial, a fake
// proxy run, and a per-user config root of its own for the copier's
// state.
type runtimeEnv struct {
	t        *testing.T
	root     string // the state dir
	folder   string // copy's destination
	copyDir  string // the copier's state folder
	files    fstest.MapFS
	held     chan struct{} // closed when a download of held.fit stalls
	heldOnce sync.Once

	mu       sync.Mutex
	sessions []*runtimeSession
	events   []string

	// gate, while set, holds every read of the run's status until it is
	// closed, and entered is told the first time a read waits on it.
	gate    atomic.Pointer[chan struct{}]
	entered chan struct{}
}

type runtimeSession struct {
	remote.Session
	closed atomic.Bool
}

func (s *runtimeSession) Close() error {
	s.closed.Store(true)
	return s.Session.Close()
}

func newRuntimeEnv(t *testing.T, startCopying bool) *runtimeEnv {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	e := &runtimeEnv{
		t: t, root: filepath.Join(t.TempDir(), "state"), folder: filepath.Join(t.TempDir(), "images"),
		copyDir: filepath.Join(base, "seestar-copy"), held: make(chan struct{}), entered: make(chan struct{}, 1),
		files: fstest.MapFS{
			"MyWorks/done.fit": {Data: make([]byte, 1<<10), ModTime: time.Unix(1761700000, 0), Mode: 0o644},
			"MyWorks/held.fit": {Data: make([]byte, 64<<10), ModTime: time.Unix(1761700000, 0), Mode: 0o644},
		},
	}
	st, err := openStorage(e.root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.directory.Close()
	set := copyapp.DefaultSettings()
	set.Folder, set.StartCopying = e.folder, startCopying
	if err := (&copyStore{storage: st}).save(set); err != nil {
		t.Fatal(err)
	}
	savedDial, savedText := copyDial, runText
	t.Cleanup(func() { copyDial, runText = savedDial, savedText })
	copyDial = func(context.Context, copyapp.Target) (remote.Session, error) {
		f := remote.NewFake(e.files)
		f.Faults = map[string]remote.Fault{"MyWorks/held.fit": {After: 1 << 10, Stall: func() { e.heldOnce.Do(func() { close(e.held) }) }}}
		s := &runtimeSession{Session: f}
		e.mu.Lock()
		e.sessions = append(e.sessions, s)
		e.mu.Unlock()
		return s, nil
	}
	runText = func(*proxy.Status) string {
		if gate := e.gate.Load(); gate != nil {
			select {
			case e.entered <- struct{}{}:
			default:
			}
			<-*gate
		}
		return "Ready"
	}
	return e
}

// stopped reports what of Browse and the copier is still running: an
// open session, a temporary file in the folder, or the folder's lock.
func (e *runtimeEnv) stopped() error {
	var problems []error
	e.mu.Lock()
	for i, s := range e.sessions {
		if !s.closed.Load() {
			problems = append(problems, errors.New("session "+string(rune('0'+i))+" is open"))
		}
	}
	e.mu.Unlock()
	filepath.WalkDir(e.folder, func(p string, d fs.DirEntry, err error) error {
		if err == nil && plan.IsTempName(d.Name()) {
			problems = append(problems, errors.New("a temporary file is left: "+d.Name()))
		}
		return nil
	})
	if st, err := state.Open(e.copyDir, e.folder, nil); err != nil {
		problems = append(problems, errors.New("the folder's lock isn't free: "+err.Error()))
	} else {
		st.Close()
	}
	return errors.Join(problems...)
}

func (e *runtimeEnv) record(event string, err error) {
	if err != nil {
		event += ": " + err.Error()
	}
	e.mu.Lock()
	e.events = append(e.events, event)
	e.mu.Unlock()
}

// run is the fake proxy: it runs until its context ends, then records
// what was still running.
func (e *runtimeEnv) run(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
	<-ctx.Done()
	e.record("proxy stopped", e.stopped())
	return nil
}

// client talks to the running UI as its page does.
type runtimeClient struct {
	t           *testing.T
	host, token string
	http        *http.Client
}

func newRuntimeClient(t *testing.T, page string) *runtimeClient {
	u, err := url.Parse(page)
	if err != nil {
		t.Fatal(err)
	}
	return &runtimeClient{t: t, host: u.Host, token: u.Fragment, http: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 30 * time.Second}}
}

func (c *runtimeClient) do(method, path, body string) (int, string) {
	r, err := http.NewRequest(method, "http://"+c.host+path, strings.NewReader(body))
	if err != nil {
		c.t.Error(err)
		return 0, ""
	}
	r.Header.Set("X-Seestar-Token", c.token)
	r.Header.Set("Origin", "http://"+c.host)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w, err := c.http.Do(r)
	if err != nil {
		c.t.Errorf("%s %s: %v", method, path, err)
		return 0, ""
	}
	defer w.Body.Close()
	b, _ := io.ReadAll(w.Body)
	return w.StatusCode, string(b)
}

// connect starts the fake proxy run through the account, which the
// copier then reaches the telescope through.
func (c *runtimeClient) connect() {
	body, err := json.Marshal(input{Settings: testSettings(), Password: testPassword})
	if err != nil {
		c.t.Fatal(err)
	}
	if code, reply := c.do("POST", "/api/start", string(body)); code != 200 {
		c.t.Errorf("connect = %d %s", code, reply)
	}
}

// copyState is the Copy tab's state.
func (c *runtimeClient) copyState() string {
	_, reply := c.do("GET", "/copy/api/status", "")
	var status struct {
		Copy struct {
			State string `json:"state"`
		} `json:"copy"`
	}
	json.Unmarshal([]byte(reply), &status)
	return status.Copy.State
}

// browse lists the telescope's folder, which leaves Browse a session open.
func (c *runtimeClient) browse() {
	if code, reply := c.do("GET", "/browse/api/list?path=MyWorks", ""); code != 200 || !strings.Contains(reply, "held.fit") {
		c.t.Errorf("browse = %d %s", code, reply)
	}
}

// launch runs the UI with drive as its window, which gets a client and
// the window's close request, and returns the order things stopped in.
func (e *runtimeEnv) launch(ctx context.Context, drive func(*runtimeClient, func())) []string {
	t := e.t
	window := func(ctx context.Context, page string, quit func(), _ <-chan struct{}) error {
		drive(newRuntimeClient(t, page), quit)
		<-ctx.Done()
		e.record("window ended", e.stopped())
		return nil
	}
	finished := make(chan error, 1)
	go func() {
		finished <- runUIPresented(ctx, e.root, io.Discard, io.Discard, e.run, func(string) error {
			t.Error("opened a browser")
			return nil
		}, &fakeKeychain{}, window, &copyapp.Native{})
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("runUIPresented = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the UI didn't stop: teardown is stuck")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.events)
}

func (e *runtimeEnv) waitHeld() {
	select {
	case <-e.held:
	case <-time.After(20 * time.Second):
		e.t.Fatal("copying didn't start held.fit")
	}
}

// Closing the window, or the end of the caller's context, stops Browse
// and the copier, which leave no temporary file and free the folder's
// lock, before the proxy they reach the telescope through stops and the
// window's context ends.
func TestRuntimeStopsCopyBeforeProxy(t *testing.T) {
	want := []string{"proxy stopped", "window ended"}
	for _, how := range []string{"window close", "parent cancel"} {
		t.Run(how, func(t *testing.T) {
			e := newRuntimeEnv(t, true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			got := e.launch(ctx, func(c *runtimeClient, quit func()) {
				c.connect()
				e.waitHeld()
				c.browse()
				if how == "window close" {
					quit()
				} else {
					cancel()
				}
			})
			if !slices.Equal(got, want) {
				t.Errorf("stopped as %q; want %q", got, want)
			}
			if err := e.stopped(); err != nil {
				t.Error(err)
			}
		})
	}
}

// A pass of copying waiting on admission, held by a Free up space
// preview that waits on the target, doesn't keep the window from
// closing, and everything still stops before the proxy.
func TestRuntimeStopsWithPassWaitingOnAdmission(t *testing.T) {
	e := newRuntimeEnv(t, true)
	delete(e.files, "MyWorks/held.fit")
	var requests sync.WaitGroup
	got := e.launch(t.Context(), func(c *runtimeClient, quit func()) {
		c.connect()
		deadline := time.Now().Add(20 * time.Second)
		for c.copyState() != "uptodate" {
			if time.Now().After(deadline) {
				t.Error("the first pass didn't finish")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		c.do("GET", "/browse/api/list?path=MyWorks", "")
		gate := make(chan struct{})
		e.gate.Store(&gate)
		requests.Go(func() { c.do("POST", "/copy/api/delete", `{"action":"find","days":"7"}`) })
		select {
		case <-e.entered:
		case <-time.After(20 * time.Second):
			t.Error("the preview didn't ask for the target")
		}
		// The next pass now waits on the preview's admission.
		requests.Go(func() { c.do("POST", "/copy/api/copy", `{"action":"check"}`) })
		time.Sleep(50 * time.Millisecond)
		quit()
		time.AfterFunc(100*time.Millisecond, func() { close(gate) })
	})
	requests.Wait()
	if want := []string{"proxy stopped", "window ended"}; !slices.Equal(got, want) {
		t.Errorf("stopped as %q; want %q", got, want)
	}
}

// A failed start never starts copying, and its mount has stopped before
// ui.lock is released.
func TestRuntimeStartupFailureStartsNoCopying(t *testing.T) {
	e := newRuntimeEnv(t, true)
	st, err := openStorage(e.root)
	if err != nil {
		t.Fatal(err)
	}
	set := defaultSettings()
	set.Route, set.LANAddress = routeLAN, "192.0.2.20:445"
	if err := st.write("config.json", set); err != nil {
		t.Fatal(err)
	}
	st.directory.Close()
	// The runtime record can't replace a directory.
	d, err := privatefile.Directory(filepath.Join(e.root, "ui-runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	err = runUIPresented(t.Context(), e.root, io.Discard, io.Discard, e.run, func(string) error {
		t.Error("opened a browser")
		return nil
	}, &fakeKeychain{}, nil, nil)
	if err == nil {
		t.Fatal("startup didn't fail")
	}
	time.Sleep(time.Second)
	e.mu.Lock()
	dialed := len(e.sessions)
	e.mu.Unlock()
	if dialed != 0 {
		t.Errorf("copying dialed the telescope %d times after startup failed", dialed)
	}
	if err := e.stopped(); err != nil {
		t.Error(err)
	}
}
