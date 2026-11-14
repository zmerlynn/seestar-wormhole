package cli

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/testlog"
)

// watchedRemover is watchedWalk for a session a real delete removes
// through.
type watchedRemover struct {
	remote.Remover
	at func(p string)
}

func (s watchedRemover) Walk(ctx context.Context, skip func(string, bool) bool, kept func(remote.Entry)) ([]remote.Entry, error) {
	return s.Remover.Walk(ctx, func(p string, dir bool) bool {
		s.at(p)
		return skip(p, dir)
	}, kept)
}

// A real delete, too, shows each entry an exclude skipped while the share
// is still being listed, and only once.
func TestRealDeleteRecordsWhileListing(t *testing.T) {
	share := newWritableShare(t)
	share.put(t, ".DS_Store", "x", deleteOld)
	share.put(t, "MyWorks/a.fit", "aaa", deleteOld)
	share.put(t, "zz/b.fit", "bbb", deleteOld)
	var stderr testlog.Buffer
	var seen string
	old := dialRemover
	t.Cleanup(func() { dialRemover = old; readonly.Reset() })
	dialRemover = func(context.Context, string, *slog.Logger) (remote.Remover, error) {
		s, err := remote.NewFakeDir(share.dir)
		if err != nil {
			return nil, err
		}
		return watchedRemover{s, func(p string) {
			if p == "zz" {
				seen = stderr.String()
			}
		}}, nil
	}
	code := run(t.Context(), []string{"delete", "--endpoint", "h:445", "--force", "--ignore-state", "--include", "*.fit"}, new(strings.Builder), &stderr)
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, stderr.String())
	}
	skip := `msg=skipped path=.DS_Store exclude=.*`
	at := 0
	for _, w := range []string{`msg=connecting endpoint=h:445`, `msg="listing the share"`, skip} {
		i := strings.Index(seen[at:], w)
		if i < 0 {
			t.Fatalf("before the listing ended, lacks %s in order:\n%s\nthe whole run:\n%s", w, seen, stderr.String())
		}
		at += i + len(w)
	}
	if n := strings.Count(stderr.String(), skip); n != 1 {
		t.Errorf("%d records of %s, want 1:\n%s", n, skip, stderr.String())
	}
}
