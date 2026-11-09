package app

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// shortText is what a transfer that ended early says, in an alert, the
// log and Activity: the byte counts stay private with the rest.
const shortText = "the device sent fewer bytes than expected"

// A save of one file that the device ends early says so.
func TestSaveShortReadSaysWhy(t *testing.T) {
	e, f := newNativeEnv(t)
	f.answer = filepath.Join(t.TempDir(), "saved.txt")
	e.s.dial = func(context.Context, Target) (remote.Session, error) {
		s := remote.NewFake(fstest.MapFS{"at 192.0.2.10_445/x.txt": {Data: []byte("abc")}})
		s.Faults = map[string]remote.Fault{"at 192.0.2.10_445/x.txt": {After: 1, ReadErr: io.EOF}}
		return s, nil
	}
	e.serve("GET", xURL, "")
	if want := "Couldn't save x.txt: " + shortText + ". Try saving it again."; len(f.alerts) != 1 || f.alerts[0] != want {
		t.Errorf("alerts %q, want %q", f.alerts, want)
	}
	if log := e.logs.String(); !strings.Contains(log, `err="`+shortText+`"`) {
		t.Errorf("log:\n%s", log)
	}
}

// A copied file that the device ends early says so in Activity.
func TestCopyShortReadSaysWhy(t *testing.T) {
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc")})
	e.set.Once = true
	e.faults = map[string]remote.Fault{"MyWorks/a.jpg": {After: 1, ReadErr: io.EOF}}
	if err := e.c.start(e.set, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	e.waitOff()
	if v := e.wait(stDone); len(v.Failed) != 1 {
		t.Errorf("failed %+v", v.Failed)
	}
	lines, _ := e.c.activity.lines()
	var activity strings.Builder
	for _, l := range lines {
		activity.WriteString(l.Text + "\n")
	}
	if !strings.Contains(activity.String(), shortText) {
		t.Errorf("Activity lacks %q:\n%s", shortText, activity.String())
	}
}

// growingSession lists every file one byte shorter than it reads, as a
// file that grows on the device would.
type growingSession struct{ remote.Session }

func (s growingSession) Open(ctx context.Context, p string) (remote.File, error) {
	f, err := s.Session.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return growingFile{f}, nil
}

type growingFile struct{ remote.File }

func (f growingFile) Stat() (fs.FileInfo, error) {
	fi, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return shorterInfo{fi}, nil
}

type shorterInfo struct{ fs.FileInfo }

func (i shorterInfo) Size() int64 { return i.FileInfo.Size() - 1 }

// A save of one file that the device sends more of than it listed saves
// nothing, and doesn't say the device sent fewer bytes.
func TestSaveLongerReadSaysWhy(t *testing.T) {
	e, f := newNativeEnv(t)
	f.answer = filepath.Join(t.TempDir(), "saved.txt")
	e.s.dial = func(context.Context, Target) (remote.Session, error) {
		return growingSession{remote.NewFake(fstest.MapFS{"at 192.0.2.10_445/x.txt": {Data: []byte("abc")}})}, nil
	}
	e.serve("GET", xURL, "")
	if _, err := os.Stat(f.answer); !os.IsNotExist(err) {
		t.Errorf("saved a file longer than listed: %v", err)
	}
	if want := "Couldn't save x.txt: the device sent more bytes than expected. Try saving it again."; len(f.alerts) != 1 || f.alerts[0] != want {
		t.Errorf("alerts %q, want %q", f.alerts, want)
	}
}
