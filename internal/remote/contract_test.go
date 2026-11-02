package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// The contract tests run against every Session: the Fake in the default
// tests and the Samba fixture under the smbintegration tag. Both serve the
// tree described by testdata/samba/tree.txt.

const manifestPath = "testdata/samba/tree.txt"

// fixture is the parsed manifest, in fs.WalkDir order.
func fixture(t testing.TB) []Entry {
	t.Helper()
	f, err := os.Open(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var entries []Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 {
			t.Fatalf("manifest line %q", line)
		}
		mtime, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		e := Entry{Path: fields[2], ModTime: time.Unix(mtime, 0), Kind: Dir}
		if fields[0] != "dir" {
			if e.Size, err = strconv.ParseInt(fields[0], 10, 64); err != nil {
				t.Fatal(err)
			}
			e.Kind = Regular
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

// content is what a fixture file of the given size holds: the first size
// bytes of `seq -w 0 9999999`.
func content(size int64) []byte {
	b := make([]byte, 0, size+8)
	for i := 0; int64(len(b)) < size; i++ {
		b = fmt.Appendf(b, "%07d\n", i)
	}
	return b[:size]
}

func fixtureFile(t *testing.T, path string) Entry {
	t.Helper()
	for _, e := range fixture(t) {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("%s is not in the fixture", path)
	return Entry{}
}

// cancelingWriter cancels after its first write and records what it got.
type cancelingWriter struct {
	cancel context.CancelFunc
	writes int
	first  int
	total  int64
}

func (w *cancelingWriter) Write(p []byte) (int, error) {
	if w.writes == 0 {
		w.first = len(p)
		w.cancel()
	}
	w.writes++
	w.total += int64(len(p))
	return len(p), nil
}

// runContract checks the behavior every Session shares. dial returns a fresh
// session on the fixture; runContract closes it.
func runContract(t *testing.T, dial func(t *testing.T) Session) {
	all := fixture(t)
	session := dial(t)
	t.Cleanup(func() { _ = session.Close() })
	ctx := t.Context()

	t.Run("WalkRoot", func(t *testing.T) {
		got, err := session.Walk(ctx, nil)
		if err != nil {
			t.Fatalf("Walk: %v", err)
		}
		sameEntries(t, got, all)
	})

	t.Run("WalkSkip", func(t *testing.T) {
		// A skipped folder is left out with everything below it, a
		// skipped file alone; its later siblings are still listed. The
		// root itself is never offered to skip.
		skipped := map[string]bool{"MyWorks": true, "Autorun/Dark/Dark_10.0s_IRCUT_20261029-020000.fit": true}
		var asked []string
		got, err := session.Walk(ctx, func(p string, dir bool) bool {
			asked = append(asked, fmt.Sprintf("%s %t", p, dir))
			return skipped[p]
		})
		if err != nil {
			t.Fatalf("Walk with skip: %v", err)
		}
		var want, wantAsked []Entry
		for _, e := range all {
			if !strings.HasPrefix(e.Path, "MyWorks/") {
				wantAsked = append(wantAsked, e)
				if !skipped[e.Path] {
					want = append(want, e)
				}
			}
		}
		sameEntries(t, got, want)
		if len(asked) != len(wantAsked) {
			t.Errorf("skip asked about %q, want every listed entry above a skipped folder", asked)
		}
		for i := range min(len(asked), len(wantAsked)) {
			if want := fmt.Sprintf("%s %t", wantAsked[i].Path, wantAsked[i].Kind == Dir); asked[i] != want {
				t.Errorf("skip asked about %q, want %q", asked[i], want)
			}
		}
	})

	t.Run("InvalidPaths", func(t *testing.T) {
		for _, p := range []string{".", "", "/MyWorks", "MyWorks/", "MyWorks//case", "..", "MyWorks/../MyWorks", "./MyWorks", `MyWorks\case`,
			"/MyWorks2/y.fit", "MyWorks2//y.fit", "MyWorks2/./y.fit", `MyWorks2\y.fit`, "MyWorks2/y.fit/"} {
			if _, err := session.Open(ctx, p); !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("Open(%q) = %v, want fs.ErrInvalid", p, err)
			}
		}
	})

	t.Run("OpenAndCopy", func(t *testing.T) {
		for _, e := range all {
			if e.Kind != Regular {
				continue
			}
			f, err := session.Open(ctx, e.Path)
			if err != nil {
				t.Fatalf("Open(%q): %v", e.Path, err)
			}
			h := sha256.New()
			// sha256's hash has no ReadFrom, so io.Copy goes through WriteTo.
			n, err := io.Copy(h, f)
			if cerr := f.Close(); cerr != nil {
				t.Errorf("Close(%q): %v", e.Path, cerr)
			}
			if err != nil || n != e.Size {
				t.Fatalf("copy of %q = %d, %v; want %d bytes", e.Path, n, err, e.Size)
			}
			if want := sha256.Sum256(content(e.Size)); !bytes.Equal(h.Sum(nil), want[:]) {
				t.Errorf("%q: SHA-256 mismatch", e.Path)
			}
		}
	})

	t.Run("Read", func(t *testing.T) {
		e := fixtureFile(t, "MyWorks/M 31/Light_M 31_10.0s_IRCUT_20261029-033129.fit")
		f, err := session.Open(ctx, e.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		got, err := io.ReadAll(struct{ io.Reader }{f})
		if err != nil || !bytes.Equal(got, content(e.Size)) {
			t.Errorf("ReadAll = %d bytes, %v; want the fixture's %d", len(got), err, e.Size)
		}
	})

	t.Run("OpenMissing", func(t *testing.T) {
		_, err := session.Open(ctx, "MyWorks/case/b.fit")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open of a missing file = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("CancelMidFile", func(t *testing.T) {
		e := fixtureFile(t, "MyWorks/big/Stacked_big.fit")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f, err := session.Open(ctx, e.Path)
		if err != nil {
			t.Fatal(err)
		}
		w := &cancelingWriter{cancel: cancel}
		n, err := f.WriteTo(w)
		_ = f.Close()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("WriteTo after cancel = %v, want context.Canceled", err)
		}
		if n >= e.Size || n != w.total {
			t.Errorf("WriteTo wrote %d of %d bytes (writer saw %d)", n, e.Size, w.total)
		}
		if _, err := f.Read(make([]byte, 1)); err == nil {
			t.Error("Read after cancel succeeded")
		}
	})

	t.Run("Concurrent", func(t *testing.T) {
		// Copies overlap on one session: every file is opened before any
		// copy starts. One copy of the big file is canceled after its first
		// write while two others and every small file run to the end.
		s := dial(t)
		big := fixtureFile(t, "MyWorks/big/Stacked_big.fit")
		jobs := []Entry{big, big}
		for _, e := range all {
			if e.Kind == Regular && e.Path != big.Path {
				jobs = append(jobs, e)
			}
		}
		var opened, finished sync.WaitGroup
		start := make(chan struct{})
		copyFile := func(ctx context.Context, e Entry, w io.Writer) (int64, error) {
			f, err := s.Open(ctx, e.Path)
			opened.Done()
			if err != nil {
				return 0, err
			}
			defer f.Close()
			<-start
			return f.WriteTo(w)
		}
		opened.Add(len(jobs) + 1)
		for _, e := range jobs {
			finished.Go(func() {
				h := sha256.New()
				n, err := copyFile(ctx, e, h)
				if want := sha256.Sum256(content(e.Size)); err != nil || n != e.Size || !bytes.Equal(h.Sum(nil), want[:]) {
					t.Errorf("concurrent copy of %q = %d, %v; want %d bytes with the fixture's SHA-256", e.Path, n, err, e.Size)
				}
			})
		}
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		w := &cancelingWriter{cancel: cancel}
		var canceledN int64
		var canceledErr error
		finished.Go(func() { canceledN, canceledErr = copyFile(cctx, big, w) })
		opened.Wait()
		close(start)
		finished.Wait()
		if !errors.Is(canceledErr, context.Canceled) || canceledN >= big.Size || canceledN != w.total {
			t.Errorf("canceled copy = %d, %v (writer saw %d); want context.Canceled before %d bytes", canceledN, canceledErr, w.total, big.Size)
		}
		if err := s.Close(); err != nil {
			t.Errorf("Close after concurrent copies: %v", err)
		}
	})

	t.Run("CanceledWalk", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := session.Walk(ctx, nil); !errors.Is(err, context.Canceled) {
			t.Errorf("Walk with a canceled context = %v", err)
		}
	})

	t.Run("Close", func(t *testing.T) {
		s := dial(t)
		f, err := s.Open(ctx, "MyWorks2/y.fit")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
		if _, err := s.Walk(ctx, nil); err == nil {
			t.Error("Walk after Close succeeded")
		}
		if _, err := s.Open(ctx, "MyWorks2/y.fit"); err == nil {
			t.Error("Open after Close succeeded")
		}
		if _, err := f.Read(make([]byte, 1)); err == nil {
			t.Error("Read of a file opened before Close succeeded")
		}
		_ = f.Close()
	})
}

// fixtureFS is the manifest tree as an fstest.MapFS.
func fixtureFS(t testing.TB) fstest.MapFS {
	fsys := fstest.MapFS{}
	for _, e := range fixture(t) {
		if e.Kind == Dir {
			fsys[e.Path] = &fstest.MapFile{Mode: fs.ModeDir | 0o755, ModTime: e.ModTime}
		} else {
			fsys[e.Path] = &fstest.MapFile{Data: content(e.Size), Mode: 0o644, ModTime: e.ModTime}
		}
	}
	return fsys
}

func TestFakeContract(t *testing.T) {
	fsys := fixtureFS(t)
	runContract(t, func(*testing.T) Session { return NewFake(fsys) })
}

func TestContent(t *testing.T) {
	if got := content(16); string(got) != "0000000\n0000001\n" {
		t.Fatalf("content(16) = %q", got)
	}
}
