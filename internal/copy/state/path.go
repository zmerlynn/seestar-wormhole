package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Path returns where the state of dest lives:
// <stateDir>/sync/<lowercase hex SHA-256 of the absolute, cleaned dest>.jsonl.
// The path is derived from the string alone: it does not resolve symbolic
// links or fold case. It is an error for the state file to lie inside dest;
// [Open] also refuses a state directory that resolves to a place inside it,
// or that has an existing ancestor that is dest's folder under another
// spelling.
func Path(stateDir, dest string) (string, error) {
	p, _, err := resolve(stateDir, dest)
	return p, err
}

// resolve returns the state path and the absolute, cleaned dest.
func resolve(stateDir, dest string) (path, absDest string, err error) {
	if stateDir == "" {
		return "", "", errors.New("state directory is empty")
	}
	if dest == "" {
		return "", "", errors.New("destination is empty")
	}
	absDest, err = filepath.Abs(dest)
	if err != nil {
		return "", "", fmt.Errorf("resolve destination %s: %w", dest, err)
	}
	absState, err := filepath.Abs(stateDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve state directory %s: %w", stateDir, err)
	}
	sum := sha256.Sum256([]byte(absDest))
	path = filepath.Join(absState, "sync", hex.EncodeToString(sum[:])+".jsonl")
	if inside(absDest, path) {
		return "", "", fmt.Errorf("state directory %s is inside the destination %s", absState, absDest)
	}
	return path, absDest, nil
}

// inside reports whether path is dir or lies under it, judged by the strings.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkOutside refuses any of dirs that is inside absDest, which must exist.
// It catches what the string check in resolve cannot. First it compares
// resolved paths, which finds a state directory that is a symbolic link into
// the destination; a directory that does not exist yet is resolved through
// its deepest existing ancestor. Then it compares the destination by
// [os.SameFile] with each existing ancestor of the resolved path, which finds
// a spelling that resolves to a different string for the same folder, such
// as another case on a case-insensitive filesystem or a bind mount of the
// whole destination.
func checkOutside(absDest string, dirs ...string) error {
	realDest, err := filepath.EvalSymlinks(absDest)
	if err != nil {
		return fmt.Errorf("resolve destination %s: %w", absDest, err)
	}
	destInfo, err := os.Stat(realDest)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	for _, dir := range dirs {
		real, err := resolveExisting(dir)
		if err != nil {
			return fmt.Errorf("resolve state directory %s: %w", dir, err)
		}
		if inside(realDest, real) {
			return fmt.Errorf("state directory %s is inside the destination %s: it resolves to %s, inside %s", dir, absDest, real, realDest)
		}
		same, err := ancestorSameAs(real, destInfo)
		if err != nil {
			return fmt.Errorf("compare state directory %s with the destination %s: %w", dir, absDest, err)
		}
		if same != "" {
			return fmt.Errorf("state directory %s is inside the destination %s: its ancestor %s is the same folder as the destination", dir, absDest, same)
		}
	}
	return nil
}

// ancestorSameAs returns the first of p and its ancestors that exists and is
// the same file as target, or "" if none is.
func ancestorSameAs(p string, target fs.FileInfo) (string, error) {
	for {
		fi, err := os.Stat(p)
		if err == nil && os.SameFile(fi, target) {
			return p, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", nil
		}
		p = parent
	}
}

// resolveExisting resolves the symbolic links of the deepest existing
// ancestor of the absolute, clean path p, and appends the rest of p. A
// dangling link is an error, since creating through it would land wherever
// it points.
func resolveExisting(p string) (string, error) {
	rest := ""
	for {
		_, err := os.Lstat(p)
		if err == nil {
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				return "", err
			}
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}
