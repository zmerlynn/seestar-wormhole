package proxy

import (
	"context"
	"github.com/rclone/rclone/fs"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

func TestPullComposedSMB(t *testing.T) {
	for _, via := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "via"}[via], func(t *testing.T) { testPullComposedSMB(t, via) })
	}
}

func testPullComposedSMB(t *testing.T, via bool) {
	addr := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if addr == "" {
		t.Skip("run scripts/test-pull-container.sh in the Go container")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cloud := &simulatedCloud{}
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: cloud})
	if err != nil {
		t.Fatal(err)
	}
	var trace []string
	s, err := start(ctx, Config{HoldOnDemand: true}, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
			p := newSimulatedWire(t, &trace)
			p.sambaAddress = addr
			return p
		}}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := pull.DefaultConfig()
	c.Destination = t.TempDir()
	c.Settle = 0
	d, err := pull.OpenDestination(c.Destination)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.BindSource("synthetic-device"); err != nil {
		t.Fatal(err)
	}
	ctx = pull.Configure(ctx, c)
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) { return s.DialPort(ctx, 445) }
	if via {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		serving := make(chan error, 1)
		go func() { serving <- Serve(ctx, s, []Forward{{Listener: listener, Port: 445}}, 16, nil) }()
		defer func() {
			cancel()
			if err := <-serving; err != nil {
				t.Error(err)
			}
		}()
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		}
	}
	ctx, src, err := pull.OpenSMB(ctx, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Features().Shutdown(context.Background())
	var reads, bytes atomic.Int64
	src = pullReadFs{Fs: src, reads: &reads, bytes: &bytes}
	b, err := d.Run(ctx, c, time.Now().UTC(), src)
	if err != nil {
		t.Fatal(err)
	}
	if b.Result.Copied != 2 || reads.Load() != 2 || bytes.Load() != 131100 {
		t.Fatalf("first=%+v opens=%d source bytes=%d want=131100", b.Result, reads.Load(), bytes.Load())
	}
	// Both overlap and explicit-since repeat runs must do zero SMB file opens.
	c.Overlap = 24 * time.Hour
	reads.Store(0)
	bytes.Store(0)
	second, err := d.Run(ctx, c, time.Now().UTC(), src)
	if err != nil {
		t.Fatal(err)
	}
	if second.Result.Copied != 0 || second.Result.Verified != 0 || reads.Load() != 0 || bytes.Load() != 0 || second.Result.Status != "nothing-new" {
		t.Fatalf("second=%+v", second.Result)
	}
	c.Since = "24h"
	reads.Store(0)
	bytes.Store(0)
	explicit, err := d.Run(ctx, c, time.Now().UTC(), src)
	if err != nil || explicit.Result.Verified != 0 || reads.Load() != 0 || bytes.Load() != 0 {
		t.Fatalf("explicit repeat: batch=%+v reads=%d err=%v", explicit, reads.Load(), err)
	}
	c.Recheck = true
	recheck, err := d.Run(ctx, c, time.Now().UTC(), src)
	if err != nil || recheck.Result.Verified != 2 || reads.Load() != 2 || bytes.Load() != 131100 {
		t.Fatalf("recheck: batch=%+v reads=%d err=%v", recheck, reads.Load(), err)
	}
	if err = src.Features().Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if via {
		if cloud.stops.Load() != 0 {
			t.Fatal("SMB client released ambient owner")
		}
		// Client shutdown leaves the ambient session usable for another client.
		control, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		control.Close()
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = b.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if cloud.logins.Load() != 1 || cloud.allocs.Load() != 1 || cloud.stops.Load() != 1 {
		t.Fatalf("login/allocation/stop=%d/%d/%d", cloud.logins.Load(), cloud.allocs.Load(), cloud.stops.Load())
	}
}

// File opens are counted at the standard SMB API, never by parsing its wire.
type pullReadFs struct {
	fs.Fs
	reads *atomic.Int64
	bytes *atomic.Int64
}

func (f pullReadFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	entries, err := f.Fs.List(ctx, dir)
	for i, e := range entries {
		if o, ok := e.(fs.Object); ok {
			entries[i] = pullReadObject{o, f.reads, f.bytes}
		}
	}
	return entries, err
}
func (f pullReadFs) NewObject(ctx context.Context, name string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil {
		return nil, err
	}
	return pullReadObject{o, f.reads, f.bytes}, nil
}

type pullReadObject struct {
	fs.Object
	reads *atomic.Int64
	bytes *atomic.Int64
}

func (o pullReadObject) Open(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
	o.reads.Add(1)
	r, err := o.Object.Open(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return pullByteReader{r, o.bytes}, nil
}

type pullByteReader struct {
	io.ReadCloser
	bytes *atomic.Int64
}

func (r pullByteReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytes.Add(int64(n))
	return n, err
}
