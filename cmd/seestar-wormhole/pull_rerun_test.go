package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
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
)

// Permit five complete copies, then hold every subsequent copy after a real
// 16 KiB Samba read. Waiting for persisted receipts gives the interrupt/rerun
// the same completed/partial mix as run45 without a wall-clock signal delay.
type rerunFs struct {
	fs.Fs
	dest         string
	work         context.Context
	interrupt    bool
	opens, bytes atomic.Int64
	once         sync.Once
}

func (f *rerunFs) NewObject(ctx context.Context, name string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	return rerunObject{o, f}, err
}

type rerunObject struct {
	fs.Object
	f *rerunFs
}

func (o rerunObject) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	held := o.f.interrupt && o.f.opens.Add(1) > 5
	if held {
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			data, err := os.ReadFile(filepath.Join(o.f.dest, ".seestar-wormhole", "state.json"))
			var state pull.State
			if err == nil && json.Unmarshal(data, &state, json.MatchCaseInsensitiveNames(true)) == nil && len(state.Files) >= 5 {
				break
			}
			runtime.Gosched()
		}
	}
	r, err := o.Object.Open(ctx, options...)
	if err != nil {
		return nil, err
	}
	return &rerunReader{ReadCloser: r, f: o.f, held: held}, nil
}

type rerunReader struct {
	io.ReadCloser
	f             *rerunFs
	held, started bool
}

func (r *rerunReader) Read(p []byte) (int, error) {
	if r.held && r.started {
		r.f.once.Do(func() { fmt.Println("rerun-ready") })
		<-r.f.work.Done()
		return 0, r.f.work.Err()
	}
	r.started = true
	n, err := r.ReadCloser.Read(p[:min(len(p), 16384)])
	r.f.bytes.Add(int64(n))
	return n, err
}

func runRerunPull(t *testing.T, ctx context.Context, dest string, transfers int, via string, interrupt bool, out, logs io.Writer) (int, int64) {
	t.Helper()
	args := []string{"pull", "--settle=0", "--timeout=15s", fmt.Sprintf("--transfers=%d", transfers), dest}
	if via != "" {
		args = append(args, "--via="+via)
	}
	s := &pullSessionFixture{addr: os.Getenv("SEESTAR_TEST_MANY_SAMBA_ADDR")}
	var source *rerunFs
	err := runPullWithSource(ctx, args, out, logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
		if via != "" {
			t.Fatal("via constructed a session")
		}
		return s, nil
	}, func(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
		ctx, backend, err := pull.OpenSMB(ctx, dial)
		source = &rerunFs{Fs: backend, dest: dest, work: ctx, interrupt: interrupt}
		return ctx, source, err
	})
	if s.closed != boolInt(via == "") {
		t.Fatalf("release count=%d via=%s", s.closed, via)
	}
	return pullCode(err), source.bytes.Load()
}

func TestPullReceiptInterruptHelper(t *testing.T) {
	dest := os.Getenv("SEESTAR_TEST_RECEIPT_DEST")
	if dest == "" {
		return
	}
	transfers, _ := strconv.Atoi(os.Getenv("SEESTAR_TEST_RECEIPT_TRANSFERS"))
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt)
	defer stop()
	code, _ := runRerunPull(t, ctx, dest, transfers, os.Getenv("SEESTAR_TEST_RECEIPT_VIA"), true, os.Stdout, os.Stderr)
	os.Exit(code)
}

func TestPullReceiptInterruptRerun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native console signal gate required")
	}
	ep := os.Getenv("SEESTAR_TEST_MANY_SAMBA_ADDR")
	if ep == "" {
		t.Skip("16-file Samba fixture required")
	}
	for _, transfers := range []int{4, 8} {
		for _, via := range []bool{false, true} {
			t.Run(fmt.Sprintf("transfers=%d/via=%t", transfers, via), func(t *testing.T) {
				dest := t.TempDir()
				endpoint := ""
				if via {
					endpoint = signalSMBProxy(t, ep)
				}
				cmd := pullChildCommand(t, "TestPullReceiptInterruptHelper")
				cmd.Env = append(os.Environ(), "SEESTAR_TEST_RECEIPT_DEST="+dest, fmt.Sprintf("SEESTAR_TEST_RECEIPT_TRANSFERS=%d", transfers), "SEESTAR_TEST_RECEIPT_VIA="+endpoint)
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var output, logs bytes.Buffer
				cmd.Stderr = &logs
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				scanner := bufio.NewScanner(stdout)
				sent := false
				for scanner.Scan() {
					line := scanner.Text()
					fmt.Fprintln(&output, line)
					if line == "rerun-ready" {
						sent = true
						if err := cmd.Process.Signal(os.Interrupt); err != nil {
							t.Fatal(err)
						}
					}
				}
				err = cmd.Wait()
				var exit *exec.ExitError
				if !sent || !errors.As(err, &exit) || exit.ExitCode() != 130 || !strings.Contains(output.String(), "status=canceled") {
					t.Fatalf("interrupt err=%v stdout=%s logs=%s", err, &output, &logs)
				}
				d, err := pull.OpenDestination(dest)
				if err != nil {
					t.Fatal(err)
				}
				saved := len(d.State.Files)
				if saved != 5 || len(d.State.Selections) != 0 {
					t.Fatalf("interrupt state=%+v", d.State)
				}
				prior := d.State.Files
				d.Close()
				output.Reset()
				logs.Reset()
				code, sourceBytes := runRerunPull(t, t.Context(), dest, transfers, endpoint, false, &output, &logs)
				if code != 0 || sourceBytes != int64(16-saved)*131072 || !strings.Contains(output.String(), "copied=11") {
					t.Fatalf("rerun exit=%d source bytes=%d output=%s logs=%s", code, sourceBytes, &output, &logs)
				}
				d, err = pull.OpenDestination(dest)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				if len(d.State.Files) != 16 || len(d.State.Selections) != 1 {
					t.Fatalf("rerun state=%+v", d.State)
				}
				for name, r := range prior {
					if d.State.Files[name] != r {
						t.Fatalf("saved receipt changed: %s", name)
					}
				}
				for name, r := range d.State.Files {
					data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
					if err != nil || len(data) != 131072 || !bytes.Equal(data, make([]byte, 131072)) || r.Check != "size" || r.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
						t.Fatalf("file %s receipt=%+v err=%v", name, r, err)
					}
				}
				partials, err := filepath.Glob(filepath.Join(dest, ".seestar-part-*"))
				if err != nil || len(partials) != 0 {
					t.Fatalf("partials=%v err=%v", partials, err)
				}
				t.Logf("interrupt/rerun=130/0 saved=%d copied=11 source_bytes=%d transfers=%d via=%t", saved, sourceBytes, transfers, via)
			})
		}
	}
}
