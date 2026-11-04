package state

import (
	"errors"
	"os"
	"path/filepath"
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
