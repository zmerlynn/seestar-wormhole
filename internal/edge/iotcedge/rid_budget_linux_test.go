//go:build linux

package iotcedge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"golang.org/x/sys/unix"
)

// Like the CLI's rawLog, write directly: os.File's poller would wait for space.
type ridPipeLog struct{ fd, writes int }

func (w *ridPipeLog) Write(b []byte) (int, error) { w.writes++; return unix.Write(w.fd, b) }

func TestRIDHistoryBudgetFullLogPipe(t *testing.T) {
	if !ridBudgetChild(t) {
		return
	}
	processRIDs.limit = 1
	processRIDs.used = map[[8]byte]struct{}{{1}: {}}
	fds := make([]int, 2)
	if err := unix.Pipe2(fds, unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	for {
		_, err := unix.Write(fds[1], make([]byte, 4096))
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// Reopen an independent nonblocking description, as logWriter does for stderr.
	fd, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", fds[1]), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.SetNonblock(fds[1], false); err != nil {
		t.Fatal(err)
	}
	sink := &ridPipeLog{fd: fd}
	previous := slog.Default()
	logger := diagnostic.Logger(os.Getenv("WORMHOLE_RID_FORMAT"), sink, slog.LevelInfo)
	actions := make(chan iotc.Action, 1)
	actions <- iotc.Action{Kind: iotc.Generate, Attempt: 9, Token: 17}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan iotc.Event, 1)
	go func() {
		var completion iotc.Event
		(&IOTCIO{Logger: logger}).Run(ctx, actions, func(e iotc.Event) bool { completion = e; return false })
		done <- completion // Run has joined socket children and cleared crypto.
	}()
	select {
	case e := <-done:
		if e.Kind != iotc.EffectFailed || e.Attempt != 9 || e.Token != 17 || e.Material != (iotc.Material{}) {
			t.Fatalf("completion=%+v, want correlated failure without material", e)
		}
	case <-ctx.Done():
		t.Fatal("stalled diagnostic blocked failure delivery or executor join")
	}
	if sink.writes != 1 {
		t.Fatalf("owned sink writes=%d, want 1", sink.writes)
	}
	if slog.Default() != previous {
		t.Fatal("process-default logger changed")
	}
	if len(processRIDs.used) != 1 {
		t.Fatal("exhaustion changed reservations")
	}
}
