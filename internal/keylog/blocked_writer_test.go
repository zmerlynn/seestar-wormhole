//go:build unix

package keylog_test

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/edge/iotcedge"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Inject a stalled descriptor beneath the concrete writer; Open itself refuses pipes.
// This models a Write that cannot finish until storage becomes available again.
func TestBlockedKeylogCancellation(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := int(w.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := unix.Write(fd, make([]byte, 4096))
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	keys := keylog.NewTestWriter(w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actions := make(chan iotc.Action)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&iotcedge.IOTCIO{KeyLog: keys}).Run(ctx, actions, func(iotc.Event) bool { return ctx.Err() == nil })
	}()
	actions <- iotc.Action{Kind: iotc.Generate, Attempt: 1}
	// Let the cryptographic effect enter the full output descriptor.
	select {
	case <-done:
		t.Fatal("effect exited before cancellation")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	blocked := false
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		blocked = true
	}
	stack := make([]byte, 1<<20)
	n := runtime.Stack(stack, true)
	inWriter := strings.Contains(string(stack[:n]), "internal/keylog.(*Writer).Write")
	r.Close() // release injected storage stall, then join every test worker
	<-done
	keys.Close()
	if blocked && !inWriter {
		t.Fatal("probe did not observe a worker in Writer.Write")
	}
	if blocked {
		t.Fatal("IOTCIO.Run remained blocked in keylog output after cancellation; joined only after sink was unblocked")
	}
}
