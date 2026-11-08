package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveForgetsTheDestination(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	if err := s.Append(m31Copied(t)); err != nil {
		t.Fatal(err)
	}
	// While a sync holds the lock, Remove refuses and keeps the file.
	if err := Remove(stateDir, dest); !errors.Is(err, ErrLocked) {
		t.Errorf("Remove while open = %v, want ErrLocked", err)
	}
	path, _ := Path(stateDir, dest)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file after a refused Remove: %v", err)
	}
	s.Close()

	if err := Remove(stateDir, dest); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("state file after Remove: %v, want not exist", err)
	}
	records, err := Load(stateDir, dest, nil)
	if err != nil || len(records) != 0 {
		t.Errorf("Load after Remove = %v, %v; want no records", records, err)
	}
	// Removing again, or with no sync folder at all, is not an error.
	if err := Remove(stateDir, dest); err != nil {
		t.Errorf("second Remove: %v", err)
	}
	if err := Remove(filepath.Join(t.TempDir(), "none"), dest); err != nil {
		t.Errorf("Remove without a sync folder: %v", err)
	}
	// The lock is free again: a sync can open the destination.
	mustOpen(t, stateDir, dest, nil).Close()
}

// treeOf is each path under root, with a file's contents.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			got[p] = "/"
			return nil
		}
		b, err := os.ReadFile(p)
		got[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// removeRefused checks that Remove refuses stateDir as inside dest, and
// changes nothing in dest: no lock file is made and the state file there
// stays. The state file is placed where Path would put it, without Path,
// which on Windows already refuses a case alias because filepath.Rel
// ignores case there.
func removeRefused(t *testing.T, stateDir, dest string) {
	t.Helper()
	absDest, err := filepath.Abs(dest)
	if err != nil {
		t.Fatal(err)
	}
	absState, err := filepath.Abs(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(absDest))
	path := filepath.Join(absState, "sync", hex.EncodeToString(sum[:])+".jsonl")
	if p, err := Path(stateDir, dest); err == nil && p != path {
		t.Fatalf("Path = %s, the test placed the state file at %s", p, path)
	}
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := treeOf(t, dest)
	err = Remove(stateDir, dest)
	if err == nil || !strings.Contains(err.Error(), "is inside the destination") {
		t.Errorf("Remove = %v, want a refusal for a state directory inside the destination", err)
	}
	if after := treeOf(t, dest); !maps.Equal(after, before) {
		t.Errorf("Remove changed the destination: %v, was %v", after, before)
	}
}

// A state directory that resolves inside the destination, through a link,
// is refused as Open refuses it.
func TestRemoveRefusesLinkedStateInsideDest(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	mkdir(t, filepath.Join(dest, "inside"))
	link := filepath.Join(root, "state")
	symlink(t, filepath.Join(dest, "inside"), link)
	removeRefused(t, link, dest)
}

// TestRemoveRefusesStateUnderCaseAlias runs only where the temporary
// directory is on a case-insensitive filesystem, such as the default on
// macOS and Windows.
func TestRemoveRefusesStateUnderCaseAlias(t *testing.T) {
	root := t.TempDir()
	dest, alias := filepath.Join(root, "Photos"), filepath.Join(root, "photos")
	mkdir(t, dest)
	want, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.Stat(alias); err != nil || !os.SameFile(got, want) {
		t.Skip("the temporary directory is on a case-sensitive filesystem")
	}
	removeRefused(t, filepath.Join(alias, "state"), dest)
}

// A missing destination has nothing inside it: Remove removes the state
// file as before.
func TestRemoveMissingDest(t *testing.T) {
	stateDir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "gone")
	mkdir(t, dest)
	mustOpen(t, stateDir, dest, nil).Close()
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	path, _ := Path(stateDir, dest)
	if err := Remove(stateDir, dest); err != nil {
		t.Fatalf("Remove with a missing destination: %v", err)
	}
	notExist(t, path)
}
