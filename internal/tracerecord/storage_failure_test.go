package tracerecord

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
)

type failingSink struct {
	bytes.Buffer
	calls            int
	partial          bool
	writeErr         bool
	endFailure       bool
	endError         bool
	closeFailure     bool
	entered, release chan struct{}
}

func (s *failingSink) Write(b []byte) (int, error) {
	s.calls++
	if s.calls == 1 && s.entered != nil {
		close(s.entered)
		<-s.release
	}
	if s.calls == 1 && (s.partial || s.writeErr) {
		n := 0
		if s.partial {
			n, _ = s.Buffer.Write(b[:len(b)/2])
		}
		if s.writeErr {
			return n, errors.New("private storage path")
		}
		return n, nil
	}
	if s.endError && bytes.Contains(b, []byte(`"record":"end"`)) {
		return 0, errors.New("private trailer path")
	}
	if s.endFailure && bytes.Contains(b, []byte(`"record":"end"`)) {
		return s.Buffer.Write(b[:len(b)/2])
	}
	return s.Buffer.Write(b)
}
func (s *failingSink) Close() error {
	if s.closeFailure {
		return errors.New("private close path")
	}
	return nil
}

func TestStorageWriteFailureStopsSink(t *testing.T) {
	for _, tc := range []struct {
		name              string
		partial, writeErr bool
	}{
		{"short", true, false}, {"partial error", true, true}, {"zero error", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &failingSink{partial: tc.partial, writeErr: tc.writeErr, entered: make(chan struct{}), release: make(chan struct{})}
			w := New(s, SizeCap, func() time.Time { return time.Unix(0, 0) })
			w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
			<-s.entered
			for i := 0; i < 3; i++ {
				w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
			}
			close(s.release)
			w.Close()
			if got := s.calls; got != 1 {
				t.Fatalf("sink writes=%d want 1; appended after failed record", got)
			}
			if got := w.Dropped(); got != 4 {
				t.Fatalf("lost steps=%d want 4", got)
			}
			if got := w.causes.storage.Load(); got != 4 {
				t.Fatalf("storage losses=%d want 4", got)
			}
			if bytes.Contains(s.Bytes(), []byte(`"record":"end"`)) {
				t.Fatal("trailer appended after failed record")
			}
			if tc.partial && bytes.Count(s.Bytes(), []byte(`"record":"step"`)) != 1 {
				t.Fatal("missing partial first record")
			}
		})
	}
}
func TestFinalStorageFailuresCountAsLoss(t *testing.T) {
	for _, tc := range []struct {
		name               string
		end, endErr, close bool
		want               uint64
	}{
		{"trailer short", true, false, false, 1}, {"trailer error", false, true, false, 1}, {"close", false, false, true, 1}, {"both", true, false, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &failingSink{endFailure: tc.end, endError: tc.endErr, closeFailure: tc.close}
			w := New(s, SizeCap, func() time.Time { return time.Unix(0, 0) })
			w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
			w.Close()
			if got := w.Dropped(); got != tc.want {
				t.Fatalf("final storage losses=%d want %d", got, tc.want)
			}
			if got := w.causes.storage.Load(); got != tc.want {
				t.Fatalf("final storage cause=%d want %d", got, tc.want)
			}
		})
	}
}

// Verify the writer contract used by the short-write injection.
var _ io.WriteCloser = (*failingSink)(nil)
