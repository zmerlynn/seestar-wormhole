package cli

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/testlog"
)

// watchedWalk is a session whose Walk calls at with the path of each entry
// it reaches, before the walk decides on it.
type watchedWalk struct {
	*remote.Fake
	at func(p string)
}

func (s watchedWalk) Walk(ctx context.Context, skip func(string, bool) bool, kept func(remote.Entry)) ([]remote.Entry, error) {
	return s.Fake.Walk(ctx, func(p string, dir bool) bool {
		s.at(p)
		return skip(p, dir)
	}, kept)
}

// While the share is still being listed, the terminal already shows that
// the run connected, that it lists the share, and each entry an exclude
// skipped so far: in a dry run, a pass, find and a delete preview alike.
func TestRecordsWhileListing(t *testing.T) {
	files := fstest.MapFS{
		".DS_Store":     {Data: []byte("x"), ModTime: deleteOld},
		"MyWorks/a.fit": {Data: []byte("aaa"), ModTime: deleteOld},
		"zz/b.fit":      {Data: []byte("bbb"), ModTime: deleteOld},
	}
	var stderr testlog.Buffer
	var seen string
	old := dial
	t.Cleanup(func() { dial = old })
	dial = func(context.Context, string, *slog.Logger) (remote.Session, error) {
		return watchedWalk{remote.NewFake(files), func(p string) {
			if p == "zz" {
				seen = stderr.String()
			}
		}}, nil
	}
	t.Cleanup(readonly.Reset)
	for _, c := range []struct {
		args []string
		skip string
	}{
		{[]string{"sync", "--dry-run", "--dest", filepath.Join(t.TempDir(), "dest"), "--history-dir", t.TempDir()}, `msg="would skip"`},
		{[]string{"sync", "--once", "--dest", filepath.Join(t.TempDir(), "dest"), "--history-dir", t.TempDir()}, `msg=skipped`},
		{[]string{"find"}, `msg=skipped`},
		{[]string{"delete", "--ignore-state", "--include", "*.fit"}, `msg="would skip"`},
	} {
		stderr.Take()
		seen = ""
		code := run(t.Context(), append(c.args, "--endpoint", "h:445"), new(strings.Builder), &stderr)
		readonly.Reset()
		want := []string{`msg=connecting endpoint=h:445`, `msg="listing the share"`, c.skip + ` path=.DS_Store exclude=.*`}
		at := 0
		for _, w := range want {
			i := strings.Index(seen[at:], w)
			if i < 0 {
				t.Errorf("%s: exit %d; before the listing ended, lacks %s in order:\n%s\nthe whole run:\n%s", c.args, code, w, seen, stderr.String())
				break
			}
			at += i + len(w)
		}
	}
}
