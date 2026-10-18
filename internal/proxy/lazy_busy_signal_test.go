//go:build unix

package proxy

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// Real signals run in a subprocess so the test runner's signal handling remains
// intact. Cloud and transport are synthetic; start and Session.Close are real.
func TestLazyBusySignalExitStatus(t *testing.T) {
	for _, mode := range []string{"eager", "eager_busy", "lazy", "lazy_empty", "lazy_busy", "lazy_idle_busy"} {
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(mode+"/"+sig.String(), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLazyBusySignalHelper$", "-test.v")
				cmd.Env = append(os.Environ(), "LAZY_BUSY_SIGNAL_HELPER="+mode)
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				ready := false
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if scanner.Text() == "SIGNAL_READY" {
						ready = true
						if err = cmd.Process.Signal(sig); err != nil {
							t.Fatal(err)
						}
					}
				}
				err = cmd.Wait()
				if !ready {
					t.Fatal("helper did not reach signal boundary")
				}
				want := 0
				got := cmd.ProcessState.ExitCode()
				if got != want {
					t.Fatalf("exit=%d want %d: %v", got, want, err)
				}
			})
		}
	}
}

type busySignalHandler struct{ events chan string }

func (h busySignalHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h busySignalHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "session_release_complete" {
		select {
		case h.events <- r.Message:
		default:
		}
	}
	return nil
}
func (h busySignalHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h busySignalHandler) WithGroup(string) slog.Handler      { return h }

func TestLazyBusySignalHelper(t *testing.T) {
	mode := os.Getenv("LAZY_BUSY_SIGNAL_HELPER")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg := Config{}
	if mode != "eager" && mode != "eager_busy" {
		cfg.Idle = time.Millisecond
	}
	events := make(chan string, 4)
	cfg.Logger = slog.New(busySignalHandler{events})
	tr := &lazyBusyCloud{route: "precheck"}
	tr.unblocked.Store(mode == "eager" || mode == "lazy" || mode == "lazy_idle_busy")
	s := lazyBusySession(t, ctx, cfg, tr)
	if mode == "eager" || mode == "lazy" || mode == "lazy_idle_busy" {
		conn, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "lazy_idle_busy" {
			conn.Close()
			<-events // accepted idle release
			tr.unblocked.Store(false)
		} else {
			defer conn.Close()
		}
	}
	if mode == "eager_busy" || mode == "lazy_busy" || mode == "lazy_idle_busy" {
		go s.DialPort(ctx, 4700)
		<-events // refusal was consumed and joined before the signal
	}
	fmt.Println("SIGNAL_READY")
	<-ctx.Done()
	// mainExitCode returns 1 iff the command/Close result is nonnil. Keep that
	// mapping observable in this subprocess, including a handled eager first refusal.
	if err := s.Close(); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
}
