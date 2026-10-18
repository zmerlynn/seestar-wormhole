package main

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPullViaValidation(t *testing.T) {
	for _, ep := range []string{"LOCALHOST:04445", "[::1]:4445", "127.0.0.1:4445"} {
		if _, err := parse([]string{"pull", "--via=" + ep, "--version"}, io.Discard); err != nil {
			t.Fatalf("valid endpoint %q: %v", ep, err)
		}
	}
	for _, ep := range []string{"", "host", "host:0", "host:65536", "host:service", "::1:4445", ":4445", "https://host:4445"} {
		if _, err := parse([]string{"pull", "--via=" + ep, "--version"}, io.Discard); err == nil {
			t.Fatalf("accepted endpoint %q", ep)
		}
	}
	for _, flag := range []string{"--serial=", "--state-dir=", "--trace=", "--keylog=", "--busy-wait=0", "--idle=0", "--stats-interval=0", "--source-id=private"} {
		if _, err := parse([]string{"pull", "--via=localhost:4445", flag, "--version"}, io.Discard); err == nil {
			t.Fatalf("accepted session flag %s", flag)
		}
	}
}
func viaRun(t *testing.T, ctx context.Context, dest, ep string, flags ...string) (int, string, string) {
	t.Helper()
	var out, logs bytes.Buffer
	args := append([]string{"pull", "--via=" + ep, "--settle=0", "--timeout=5s"}, flags...)
	args = append(args, dest)
	err := runWithSession(ctx, args, &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
		t.Fatal("via constructed device session")
		return nil, nil
	})
	return pullCode(err), out.String(), logs.String()
}
func TestPullViaEndpointDown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ep := l.Addr().String()
	l.Close()
	code, out, _ := viaRun(t, t.Context(), t.TempDir(), ep)
	if code != 1 || !strings.Contains(out, "status=error") {
		t.Fatalf("down: code=%d %s", code, out)
	}
}
func TestPullViaTimeoutIsNotBusy(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(io.Discard, c)
	}()
	code, out, _ := viaRun(t, t.Context(), t.TempDir(), l.Addr().String(), "--timeout=100ms")
	l.Close()
	<-joined
	if code != 1 || !strings.Contains(out, "status=error") {
		t.Fatalf("timeout: code=%d %s", code, out)
	}
}
func TestPullViaSMBBindingAndRebind(t *testing.T) {
	ep := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if ep == "" {
		t.Skip("container Samba fixture required")
	}
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")
	dest := t.TempDir()
	code, out, _ := viaRun(t, t.Context(), dest, ep)
	if code != 0 || !strings.Contains(out, "copied=2") {
		t.Fatalf("first: %d %s", code, out)
	}
	d, err := pull.OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	if d.State.Source != "via:"+ep || len(d.State.Files) != 2 {
		t.Fatalf("state=%+v", d.State)
	}
	d.Close()
	code, out, logs := viaRun(t, t.Context(), dest, "localhost:1445")
	if code != 2 || !strings.Contains(logs, "via") || !strings.Contains(out, "reason=source_mismatch") {
		t.Fatalf("mismatch: %d %s %s", code, out, logs)
	}
	code, out, _ = viaRun(t, t.Context(), dest, "localhost:1445", "--rebind", "--since=all")
	if code != 0 {
		t.Fatalf("rebind: %d %s", code, out)
	}
	d, err = pull.OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.State.Source != "via:localhost:1445" || len(d.State.Files) != 2 || len(d.State.Selections) != 0 {
		t.Fatalf("rebind state=%+v", d.State)
	}
}

// The forwarding proxy can accept TCP before its lazy device dial completes.
func TestPullViaLazySMB(t *testing.T) {
	ep := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if ep == "" {
		t.Skip("container Samba fixture required")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		remote, err := net.Dial("tcp", ep)
		if err != nil {
			return
		}
		defer remote.Close()
		done := make(chan struct{})
		go func() { io.Copy(remote, c); remote.(*net.TCPConn).CloseWrite(); close(done) }()
		io.Copy(c, remote)
		c.Close()
		<-done
	}()
	code, out, _ := viaRun(t, t.Context(), t.TempDir(), l.Addr().String(), "--transfers=1")
	l.Close()
	<-joined
	if code != 0 || !strings.Contains(out, "copied=2") {
		t.Fatalf("lazy: %d %s", code, out)
	}
}

func TestPullViaDirectBindingMismatchWithoutEndpoint(t *testing.T) {
	dest := t.TempDir()
	d, err := pull.OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	d.State.Source = "direct:PRIVATE_SERIAL_HASH"
	data, err := json.Marshal(d.State)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dest, ".seestar-wormhole", "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	d.Close()
	code, out, logs := viaRun(t, t.Context(), dest, "localhost:1")
	if code != 2 || !strings.Contains(out, "reason=source_mismatch") || !strings.Contains(logs, "existing_source_kind\":\"direct") || !strings.Contains(logs, "requested_source_kind\":\"via") || strings.Contains(logs+out, "PRIVATE") {
		t.Fatalf("mismatch: code=%d out=%s logs=%s", code, out, logs)
	}
	code, out, _ = viaRun(t, t.Context(), dest, "localhost:1", "--rebind")
	if code != 1 {
		t.Fatalf("failed takeover dial: %d %s", code, out)
	}
	d, err = pull.OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.State.Source != "via:localhost:1" || len(d.State.Selections) != 0 {
		t.Fatalf("takeover not persisted: %+v", d.State)
	}
}

func TestPullBandwidthDeadlineOverSMB(t *testing.T) {
	ep := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if ep == "" {
		t.Skip("container Samba fixture required")
	}
	dest := t.TempDir()
	start := time.Now()
	code, out, _ := viaRun(t, t.Context(), dest, ep, "--bwlimit=1KiB/s", "--timeout=500ms", "--transfers=1")
	if code != 1 || !strings.Contains(out, "status=error") || !strings.Contains(out, "reason=timeout") || time.Since(start) > 5*time.Second {
		t.Fatalf("deadline admitted unlimited SMB transfer: exit=%d elapsed=%v %s", code, time.Since(start), out)
	}
	d, err := pull.OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if len(d.State.Selections) != 0 || len(d.State.Files) > 1 {
		t.Fatalf("rate failure committed batch: %+v", d.State)
	}
	if _, err = os.Stat(filepath.Join(dest, "zeros.fit")); !os.IsNotExist(err) {
		t.Fatalf("rate failure promoted large file: %v", err)
	}
}
