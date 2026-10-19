package proxy

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// TestComposedSMB uses an external standard client; ordinary go test stays offline.
// scripts/test-smb.sh supplies Samba and the official rclone release binary.
func TestComposedSMB(t *testing.T) {
	addr := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if addr == "" {
		t.Skip("run scripts/test-smb.sh for the Samba/rclone fixture")
	}
	rclone := os.Getenv("SEESTAR_TEST_RCLONE")
	if rclone == "" {
		t.Fatal("SEESTAR_TEST_RCLONE is required with the Samba fixture")
	}
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cloud := &simulatedCloud{}
	client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: cloud})
	if err != nil {
		t.Fatal(err)
	}
	var trace []string
	s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
			p := newSimulatedWire(t, &trace)
			p.sambaAddress = addr
			return p
		}}
	})
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	defer s.Close()
	local, err := net.Listen("tcp", "127.0.0.2:4445")
	if err != nil {
		t.Fatal(err)
	}
	serving := make(chan error, 1)
	go func() { serving <- Serve(ctx, s, []Forward{{Listener: local, Port: 445}}, 16, nil) }()
	defer func() {
		cancel()
		if err := <-serving; err != nil {
			t.Error(err)
		}
	}()
	dst := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, rclone, append([]string{"--config", "seestar-rclone.conf"}, args...)...)
		cmd.Dir = dst
		out, err := cmd.CombinedOutput()
		t.Logf("rclone --config seestar-rclone.conf %s\n%s", strings.Join(args, " "), out)
		if err != nil {
			t.Fatalf("rclone: %v", err)
		}
		return out
	}
	run("config", "create", "seestar", "smb", "host", "127.0.0.2", "port", "4445", "user", "guest")
	listing := strings.Fields(string(run("lsf", "seestar:EMMC Images/MyWorks", "--recursive", "--files-only")))
	slices.Sort(listing)
	if !slices.Equal(listing, []string{"nested/small.fit", "zeros.fit"}) {
		t.Fatalf("listing %q, want the two fixture files", listing)
	}
	run("copy", "seestar:EMMC Images/MyWorks", "./MyWorks")
	run("check", "--download", "seestar:EMMC Images/MyWorks", "./MyWorks")
	for name, want := range map[string][]byte{"nested/small.fit": []byte("independent literal fixture\n"), "zeros.fit": make([]byte, 128<<10)} {
		got, err := os.ReadFile(filepath.Join(dst, "MyWorks", name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("content %s: %v (got %d bytes, want %d)", name, err, len(got), len(want))
		}
	}
	// Prove --download detects same-size damage, rather than only matching sizes.
	damaged := bytes.Repeat([]byte{'x'}, len("independent literal fixture\n"))
	if err := os.WriteFile(filepath.Join(dst, "MyWorks/nested/small.fit"), damaged, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, rclone, "--config", "seestar-rclone.conf", "check", "--download", "seestar:EMMC Images/MyWorks", "./MyWorks")
	cmd.Dir = dst
	out, err := cmd.CombinedOutput()
	t.Logf("same-size corruption check: %v\n%s", err, out)
	if ctx.Err() != nil || err == nil || !bytes.Contains(out, []byte("nested/small.fit: contents differ")) {
		t.Fatalf("corruption was not detected: %v", err)
	}
	cancel()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if cloud.logins.Load() != 1 || cloud.allocs.Load() != 1 || cloud.stops.Load() != 1 {
		t.Fatalf("login/allocation/stop=%d/%d/%d, want 1/1/1", cloud.logins.Load(), cloud.allocs.Load(), cloud.stops.Load())
	}
}
