package remote

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// invalidRemovePaths are paths a remove method must refuse before it looks
// at the share: the root, a leading or trailing slash, an empty, . or ..
// element, a backslash and a NUL.
var invalidRemovePaths = []string{"", ".", "/a", "a/", "a//b", "a/./b", "a/../b", "..", `a\b`, "a\x00b"}

// removeTree makes a tree for the remove tests and returns its folder and
// the listed entry of a/b.fit.
func removeTree(t *testing.T) (string, Entry) {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"a/b.fit", "a/c.jpg", "a/b", "full/x.fit"} {
		name := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 10, 1, 2, 3, 4, 500, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "a", "b.fit"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return dir, Entry{Path: "a/b.fit", Size: int64(len("a/b.fit")), ModTime: mtime, Kind: Regular}
}

// snapshot returns every path under dir with its contents, so a test can
// see that nothing changed.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			got[rel] = "dir"
			return nil
		}
		b, err := os.ReadFile(p)
		got[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func newFakeDir(t *testing.T, dir string) *FakeDir {
	t.Helper()
	f, err := NewFakeDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestRemoverRefusesInvalidPaths checks that both remove methods refuse,
// with fs.ErrInvalid and before any removal, every path Open refuses.
func TestRemoverRefusesInvalidPaths(t *testing.T) {
	dir, e := removeTree(t)
	before := snapshot(t, dir)
	f := newFakeDir(t, dir)
	for _, p := range invalidRemovePaths {
		e.Path = p
		if err := f.RemoveFile(t.Context(), e); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("RemoveFile(%q) = %v, want fs.ErrInvalid", p, err)
		}
		if err := f.RemoveDir(t.Context(), p); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("RemoveDir(%q) = %v, want fs.ErrInvalid", p, err)
		}
	}
	if _, removes := f.Counts(); removes != 0 {
		t.Errorf("%d removals reached the folder, want none", removes)
	}
	if after := snapshot(t, dir); !reflect.DeepEqual(after, before) {
		t.Errorf("tree changed:\n before %v\n after  %v", before, after)
	}
}

func TestFakeDirRemove(t *testing.T) {
	ctx := t.Context()
	t.Run("File", func(t *testing.T) {
		dir, e := removeTree(t)
		f := newFakeDir(t, dir)
		if err := f.RemoveFile(ctx, e); err != nil {
			t.Fatalf("RemoveFile: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "a", "b.fit")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a/b.fit still there: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "a", "c.jpg")); err != nil {
			t.Errorf("sibling a/c.jpg: %v", err)
		}
		if err := f.RemoveFile(ctx, e); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("second RemoveFile = %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("Changed", func(t *testing.T) {
		dir, e := removeTree(t)
		f := newFakeDir(t, dir)
		for _, c := range []Entry{
			{Path: e.Path, Size: e.Size + 1, ModTime: e.ModTime, Kind: Regular},
			{Path: e.Path, Size: e.Size, ModTime: e.ModTime.Add(100 * time.Nanosecond), Kind: Regular},
			{Path: "a/b", Size: e.Size, ModTime: e.ModTime, Kind: Regular},
			{Path: "empty", Size: 0, ModTime: e.ModTime, Kind: Regular},
		} {
			if err := f.RemoveFile(ctx, c); !errors.Is(err, ErrChanged) {
				t.Errorf("RemoveFile(%+v) = %v, want ErrChanged", c, err)
			}
		}
		if err := f.RemoveDir(ctx, "a/c.jpg"); !errors.Is(err, ErrChanged) {
			t.Errorf("RemoveDir of a file = %v, want ErrChanged", err)
		}
		if _, removes := f.Counts(); removes != 0 {
			t.Errorf("%d removals, want none", removes)
		}
	})
	t.Run("ChangedBeforeRecheck", func(t *testing.T) {
		dir, e := removeTree(t)
		f := newFakeDir(t, dir)
		f.Faults = map[string]Fault{e.Path: {BeforeRecheck: func() {
			later := e.ModTime.Add(time.Second)
			if err := os.Chtimes(filepath.Join(dir, "a", "b.fit"), later, later); err != nil {
				t.Error(err)
			}
		}}}
		if err := f.RemoveFile(ctx, e); !errors.Is(err, ErrChanged) {
			t.Errorf("RemoveFile = %v, want ErrChanged", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "a", "b.fit")); err != nil {
			t.Errorf("a/b.fit: %v", err)
		}
	})
	t.Run("Dir", func(t *testing.T) {
		dir, _ := removeTree(t)
		f := newFakeDir(t, dir)
		if err := f.RemoveDir(ctx, "full"); !errors.Is(err, ErrNotEmpty) {
			t.Errorf("RemoveDir(full) = %v, want ErrNotEmpty", err)
		}
		if err := f.RemoveDir(ctx, "empty"); err != nil {
			t.Errorf("RemoveDir(empty) = %v", err)
		}
		if err := f.RemoveDir(ctx, "empty"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("second RemoveDir(empty) = %v, want fs.ErrNotExist", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "full", "x.fit")); err != nil {
			t.Errorf("full/x.fit: %v", err)
		}
	})
	t.Run("FilledBeforeRecheck", func(t *testing.T) {
		dir, _ := removeTree(t)
		f := newFakeDir(t, dir)
		f.Faults = map[string]Fault{"empty": {BeforeRecheck: func() {
			if err := os.WriteFile(filepath.Join(dir, "empty", "new.fit"), nil, 0o644); err != nil {
				t.Error(err)
			}
		}}}
		if err := f.RemoveDir(ctx, "empty"); !errors.Is(err, ErrNotEmpty) {
			t.Errorf("RemoveDir = %v, want ErrNotEmpty", err)
		}
	})
	t.Run("Closed", func(t *testing.T) {
		dir, e := removeTree(t)
		f := newFakeDir(t, dir)
		_ = f.Fake.Close()
		if err := f.RemoveFile(ctx, e); !errors.Is(err, fs.ErrClosed) {
			t.Errorf("RemoveFile after Close = %v, want fs.ErrClosed", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		g := newFakeDir(t, dir)
		if err := g.RemoveDir(canceled, "empty"); !errors.Is(err, context.Canceled) {
			t.Errorf("RemoveDir canceled = %v, want context.Canceled", err)
		}
		if _, removes := g.Counts(); removes != 0 {
			t.Errorf("%d removals, want none", removes)
		}
	})
}

// TestSessionStaysReadOnly pins Session's method set, so a remove method
// cannot be added to it, and checks that the read-only sessions are not
// Removers.
func TestSessionStaysReadOnly(t *testing.T) {
	st := reflect.TypeFor[Session]()
	var got []string
	for m := range st.Methods() {
		got = append(got, m.Name)
	}
	if want := []string{"Close", "Open", "ReadDir", "Walk"}; !slices.Equal(got, want) {
		t.Errorf("Session methods = %v, want %v", got, want)
	}
	rt := reflect.TypeFor[Remover]()
	for _, typ := range []reflect.Type{reflect.TypeFor[*SMB](), reflect.TypeFor[*Fake]()} {
		if typ.Implements(rt) {
			t.Errorf("%v is a Remover", typ)
		}
	}
}

// TestOnlyDeleteDialsRemover fails if DialRemover is referenced in a
// non-test file under internal/ or cmd/ other than the delete command and
// the app's Free up space tab. It stops accidental use; it does not prove
// what code can reach.
func TestOnlyDeleteDialsRemover(t *testing.T) {
	const allowed = "cmd/seestar-copy-cli/delete.go or internal/app/freespace.go"
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join("..", "..", top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(p, filepath.Join("..", "..")+string(filepath.Separator)))
			if rel == "cmd/seestar-copy-cli/delete.go" || rel == "internal/app/freespace.go" {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.FuncDecl:
					// The declaration names DialRemover; its body is still inspected.
					if n.Body != nil {
						ast.Inspect(n.Body, func(m ast.Node) bool {
							if id, ok := m.(*ast.Ident); ok && id.Name == "DialRemover" {
								t.Errorf("%s: DialRemover is referenced outside %s", fset.Position(id.Pos()), allowed)
							}
							return true
						})
					}
					return false
				case *ast.Ident:
					if n.Name == "DialRemover" {
						t.Errorf("%s: DialRemover is referenced outside %s", fset.Position(n.Pos()), allowed)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
