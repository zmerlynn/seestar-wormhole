package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Barriers sit at rclone's API boundary around actual Samba operations. No SMB
// wire interpretation or scheduler-dependent file polling chooses the phase.
type signalPhaseFs struct {
	fs.Fs
	phase          string
	work           context.Context
	once           sync.Once
	opens          atomic.Int64
	shutdownWrites atomic.Int64
	shuttingDown   atomic.Bool
	copyReaders    atomic.Int64
	transfers      int
}

func (f *signalPhaseFs) barrier(phase string) {
	if phase == f.phase {
		f.once.Do(func() { fmt.Println("signal-phase-ready"); <-f.work.Done() })
	}
}
func (f *signalPhaseFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	entries, err := f.Fs.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	f.barrier("listing")
	if err = f.work.Err(); err != nil {
		return nil, err
	}
	for i, e := range entries {
		if o, ok := e.(fs.Object); ok {
			entries[i] = signalPhaseObject{o, f}
		}
	}
	return entries, nil
}
func (f *signalPhaseFs) NewObject(ctx context.Context, name string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil {
		return nil, err
	}
	return signalPhaseObject{o, f}, nil
}
func (f *signalPhaseFs) Features() *fs.Features {
	features := *f.Fs.Features()
	shutdown := features.Shutdown
	features.Shutdown = func(ctx context.Context) error {
		f.shuttingDown.Store(true)
		return shutdown(ctx)
	}
	return &features
}

// The third backend-shutdown write follows the echo header/body and begins teardown.
// Pause at this standard net.Conn boundary without interpreting SMB bytes.
type shutdownSignalConn struct {
	net.Conn
	phase *atomic.Pointer[signalPhaseFs]
}

func (c shutdownSignalConn) Write(p []byte) (int, error) {
	if f := c.phase.Load(); f != nil && f.phase == "shutdown" && f.shuttingDown.Load() && f.shutdownWrites.Add(1) == 3 {
		f.barrier("shutdown")
	}
	return c.Conn.Write(p)
}

type signalPhaseObject struct {
	fs.Object
	f *signalPhaseFs
}

func (o signalPhaseObject) Open(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
	r, err := o.Object.Open(ctx, opts...)
	if err != nil {
		return nil, err
	}
	phase := "copy"
	if o.f.opens.Add(1) == 2 {
		phase = "verify"
	}
	return &signalPhaseReader{ReadCloser: r, f: o.f, phase: phase}, nil
}

type signalPhaseReader struct {
	io.ReadCloser
	f       *signalPhaseFs
	phase   string
	started bool
	held    bool
}

func (r *signalPhaseReader) Read(p []byte) (int, error) {
	if r.f.transfers > 1 && r.started && !r.held {
		r.held = true
		if r.f.copyReaders.Add(1) == int64(r.f.transfers) {
			r.f.barrier("copy")
		}
		<-r.f.work.Done()
	}
	r.started = true
	if r.f.transfers > 1 {
		p = p[:min(len(p), 16384)]
	}
	n, err := r.ReadCloser.Read(p)
	if r.f.transfers == 1 {
		r.f.barrier(r.phase)
	}
	if canceled := r.f.work.Err(); canceled != nil {
		err = errors.Join(err, canceled)
	}
	return n, err
}

func TestPullSignalPhaseHelper(t *testing.T) {
	phase := os.Getenv("SEESTAR_TEST_SIGNAL_PHASE")
	if phase == "" {
		return
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt)
	defer stop()
	ep := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if phase == "setup" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		ep = listener.Addr().String()
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			if _, err = io.ReadFull(conn, make([]byte, 4)); err == nil {
				fmt.Println("signal-phase-ready")
				<-ctx.Done()
			}
		}()
	}
	transfers := 1
	if value := os.Getenv("SEESTAR_TEST_SIGNAL_TRANSFERS"); value != "" {
		transfers, _ = strconv.Atoi(value)
	}
	// The parent child-process watchdog bounds setup and cleanup. This scenario
	// must be canceled by SIGINT even when filesystem setup is slow.
	args := []string{"pull", "--settle=0", "--verify", fmt.Sprintf("--transfers=%d", transfers), os.Getenv("SEESTAR_TEST_SIGNAL_DEST")}
	via := os.Getenv("SEESTAR_TEST_SIGNAL_VIA") == "1"
	if via {
		if transfers > 1 {
			ep = signalSMBProxy(t, ep)
		}
		args = append(args, "--via="+ep)
	}
	s := &pullSessionFixture{addr: ep}
	if os.Getenv("SEESTAR_TEST_SIGNAL_RELEASE_FAIL") == "1" {
		s.closeErr = session.ErrStop
	}
	err := runPullWithSource(ctx, args, os.Stdout, os.Stderr, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
		if via {
			t.Fatal("via constructed session")
		}
		return s, nil
	}, func(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
		phaseOwner := new(atomic.Pointer[signalPhaseFs])
		wrappedDial := func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dial(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return shutdownSignalConn{conn, phaseOwner}, nil
		}
		ctx, source, err := pull.OpenSMB(ctx, wrappedDial)
		if err != nil {
			return ctx, source, err
		}
		f := &signalPhaseFs{Fs: source, phase: phase, work: ctx, transfers: transfers}
		phaseOwner.Store(f)
		return ctx, f, nil
	})
	fmt.Printf("released=%d\n", s.closed)
	os.Exit(pullCode(err))
}

func TestPullSIGINTDuringSMBPhases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows console signals require a platform gate")
	}
	if os.Getenv("SEESTAR_TEST_SAMBA_ADDR") == "" {
		t.Skip("container Samba fixture required")
	}
	for _, via := range []bool{false, true} {
		for _, phase := range []string{"setup", "listing", "copy", "verify", "shutdown"} {
			for _, releaseFail := range []bool{false, true} {
				if via && releaseFail {
					continue
				}
				name := fmt.Sprintf("%s/via=%t/release-failure=%t", phase, via, releaseFail)
				t.Run(name, func(t *testing.T) {
					testPullSIGINTPhase(t, phase, via, releaseFail)

				})
			}
		}
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func testPullSIGINTPhase(t *testing.T, phase string, via, releaseFail bool, extraEnv ...string) {
	t.Helper()
	cmd := pullChildCommand(t, "TestPullSignalPhaseHelper")
	dest := t.TempDir()
	cmd.Env = append(append(os.Environ(), extraEnv...), "SEESTAR_TEST_SIGNAL_PHASE="+phase, "SEESTAR_TEST_SIGNAL_DEST="+dest, fmt.Sprintf("SEESTAR_TEST_SIGNAL_VIA=%d", boolInt(via)), fmt.Sprintf("SEESTAR_TEST_SIGNAL_RELEASE_FAIL=%d", boolInt(releaseFail)))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cmd.Stderr = &logs
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	var output strings.Builder
	sent := false
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(&output, line)
		if line == "signal-phase-ready" {
			sent = true
			if err = cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
		}
	}
	err = cmd.Wait()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	want := 130
	status := "status=canceled"
	if releaseFail {
		want = 1
		status = "status=error"
	}
	released := 1
	if via {
		released = 0
	}
	if !sent || code != want || !strings.Contains(output.String(), status) || !strings.Contains(output.String(), fmt.Sprintf("released=%d", released)) {
		t.Fatalf("sent=%t exit=%d want=%d stdout=%s stderr=%s", sent, code, want, &output, &logs)
	}
	d, stateErr := pull.OpenDestination(dest)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	defer d.Close()
	if len(d.State.Selections) != 0 {
		t.Fatal("interrupt committed checkpoint")
	}
	partials, _ := filepath.Glob(filepath.Join(dest, ".seestar-part-*"))
	if len(partials) != 0 {
		t.Fatalf("interrupt retained partials: %v", partials)
	}
}
