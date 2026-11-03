package copier

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// paced is a session whose files can only be read through WriteTo, which
// writes one byte at a time, each 600 ms after the last.
type paced struct{ remote.Session }

func (s paced) Open(ctx context.Context, p string) (remote.File, error) {
	f, err := s.Session.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return pacedFile{f}, nil
}

type pacedFile struct{ remote.File }

func (pacedFile) Read([]byte) (int, error) { return 0, errors.New("Read called; WriteTo expected") }

func (f pacedFile) WriteTo(w io.Writer) (int64, error) {
	b, err := io.ReadAll(f.File)
	if err != nil {
		return 0, err
	}
	for i := range b {
		time.Sleep(600 * time.Millisecond)
		if _, err := w.Write(b[i : i+1]); err != nil {
			return int64(i), err
		}
	}
	return int64(len(b)), nil
}

// Copying reports a download's start, its bytes at most once a second and
// once at the end, through the device file's WriteTo.
func TestCopyingProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := newFixture(t, nil)
		c := fx.copier(t)
		type call struct {
			path       string
			done, size int64
		}
		var calls []call
		c.Copying = func(p string, done, size int64) { calls = append(calls, call{p, done, size}) }
		sess := paced{device(map[string]string{"MyWorks/a.fit": "abcde"})}
		results := run(c, t.Context(), sess, 1, []plan.Decision{decision(plan.Copy, "MyWorks/a.fit", 5)})
		if len(results) != 1 || results[0].Err != nil {
			t.Fatalf("got results %+v, want one copied file", results)
		}
		fx.checkFile(t, "MyWorks/a.fit", "abcde")
		// The start, then bytes land at 0.6, 1.2, 1.8, 2.4 and 3.0 s:
		// reports at 1.2 and 2.4 s, then the final one.
		want := []call{{"MyWorks/a.fit", 0, 5}, {"MyWorks/a.fit", 2, 5}, {"MyWorks/a.fit", 4, 5}, {"MyWorks/a.fit", 5, 5}}
		if !slices.Equal(calls, want) {
			t.Errorf("Copying calls: got %v, want %v", calls, want)
		}
	})
}

// A failed download still gets its final report, with the bytes it wrote.
func TestCopyingProgressFailure(t *testing.T) {
	fx := newFixture(t, nil)
	c := fx.copier(t)
	var last []int64
	c.Copying = func(_ string, done, size int64) { last = []int64{done, size} }
	sess := device(map[string]string{"MyWorks/a.fit": "abc"})
	sess.Faults = map[string]remote.Fault{"MyWorks/a.fit": {After: 2, ReadErr: io.ErrUnexpectedEOF}}
	results := run(c, t.Context(), sess, 1, []plan.Decision{decision(plan.Copy, "MyWorks/a.fit", 3)})
	fx.checkFailed(t, results, "download: stopped after 2 of 3 bytes")
	if want := []int64{2, 3}; !slices.Equal(last, want) {
		t.Errorf("last Copying call: got done, size %v, want %v", last, want)
	}
}
