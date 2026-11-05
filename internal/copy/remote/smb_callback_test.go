package remote

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hirochachacha/go-smb2/v2"
	"github.com/hirochachacha/go-smb2/v2/x/wire"
)

// sink is a log's writer that records each record and, once held, holds
// every write until release.
type sink struct {
	mu      sync.Mutex
	records []string
	hold    chan struct{} // nil: writes go straight through
}

func (s *sink) Write(b []byte) (int, error) {
	if s.hold != nil {
		<-s.hold
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, string(b))
	return len(b), nil
}

func (s *sink) logged() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.records...)
}

// checkOneRecovery checks that s holds one record, of a panic recovered
// where, without the panic value or its stack.
func checkOneRecovery(t *testing.T, s *sink, where string) {
	t.Helper()
	got := s.logged()
	if len(got) != 1 {
		t.Fatalf("logged %d records, want one recovery record: %q", len(got), got)
	}
	r := got[0]
	if !strings.Contains(r, "internal error recovered") || !strings.Contains(r, `where="`+where+`"`) ||
		strings.Contains(r, "boom") || strings.Contains(r, "smb_callback_test.go") {
		t.Errorf("record %q, want where %q and no panic value or stack", r, where)
	}
}

// useDefaultLog points slog's default at a buffer for the test, to show
// that nothing is logged there.
func useDefaultLog(t *testing.T) *bytes.Buffer {
	var b bytes.Buffer
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, nil)))
	return &b
}

// TestCloseCallbackPanic panics in the Close that a done Dial context
// runs. The process survives, the callback finishes, and the recovery is
// logged once, by its site alone, to the log Dial was given and not to the
// default log.
func TestCloseCallbackPanic(t *testing.T) {
	def := useDefaultLog(t)
	old := closeSession
	t.Cleanup(func() { closeSession = old })
	closed := make(chan struct{})
	closeSession = func(s *smb2.Session) error {
		_ = s.Close()
		close(closed)
		panic("boom")
	}
	synctest.Test(t, func(t *testing.T) {
		out := &sink{}
		p := startPeer(t, &peer{log: slog.New(slog.NewTextHandler(out, nil))})
		defer p.stop()
		ctx, cancel := context.WithCancel(t.Context())
		s := p.mustDial(t, ctx)
		cancel()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("the canceled context did not close the session")
		}
		synctest.Wait()
		checkOneRecovery(t, out, "closing the device session")
		_ = s.Close()
	})
	if def.Len() != 0 {
		t.Errorf("logged to the default log: %s", def.String())
	}
}

// TestAbortCallbackPanic panics in the Abort that a Dial canceled during
// Mount runs, while the log's writer is held. Dial still returns, so the
// abort's completion does not wait on the log; the recovery is then
// logged once, by its site alone, to the log Dial was given and not to the
// default log.
func TestAbortCallbackPanic(t *testing.T) {
	def := useDefaultLog(t)
	old := abortSession
	t.Cleanup(func() { abortSession = old })
	aborts := 0
	abortSession = func(s *smb2.Session) error {
		aborts++
		_ = s.Abort()
		panic("boom")
	}
	synctest.Test(t, func(t *testing.T) {
		out := &sink{hold: make(chan struct{})}
		p := startPeer(t, &peer{hold: []wire.Command{wire.SMB2_TREE_CONNECT}, log: slog.New(slog.NewTextHandler(out, nil))})
		defer p.stop()
		var release sync.Once
		defer release.Do(func() { close(out.hold) })
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			s, err := p.dial(ctx)
			if err == nil {
				_ = s.Close()
			}
			done <- err
		}()
		p.awaitHeld(t)
		cancel()
		if err := await(t, done, time.Second, "Dial, with its log held,"); !errors.Is(err, context.Canceled) {
			t.Errorf("Dial = %v, want context.Canceled", err)
		}
		release.Do(func() { close(out.hold) })
		synctest.Wait()
		if aborts != 1 {
			t.Errorf("aborted %d times, want once", aborts)
		}
		checkOneRecovery(t, out, "aborting the device session")
	})
	if def.Len() != 0 {
		t.Errorf("logged to the default log: %s", def.String())
	}
}
