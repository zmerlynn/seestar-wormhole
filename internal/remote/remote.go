// Package remote is a read-only view of the telescope's SMB share.
//
// A Session lists source trees and opens files for reading, and nothing
// else: there is no call that creates, changes, renames, deletes or locks
// anything on the share. Dial returns the go-smb2 implementation; NewFake
// returns one backed by an io/fs.FS for tests. Both list with fs.WalkDir
// through the same code, so the listing rules below hold for both.
//
// Paths are share-relative, slash-separated and kept byte for byte. A path
// handed to Walk or Open must be a valid io/fs path without backslashes; any
// other path is refused with fs.ErrInvalid rather than cleaned.
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"
)

// ShareName is the telescope's share. It is not configurable.
const ShareName = "EMMC Images"

// ErrSourceNotFound reports that the source tree given to Walk does not
// exist. The returned error also wraps the underlying not-exist error.
var ErrSourceNotFound = errors.New("source not found")

// Kind is the type of a listed entry.
type Kind uint8

const (
	// Other is anything that is neither a regular file nor a directory,
	// such as a symbolic link or a device. It is the zero value.
	Other Kind = iota
	// Regular is a regular file.
	Regular
	// Dir is a directory.
	Dir
)

func (k Kind) String() string {
	switch k {
	case Regular:
		return "regular"
	case Dir:
		return "dir"
	default:
		return "other"
	}
}

// Entry is one listed file or directory below a source.
type Entry struct {
	// Path is share-relative with forward slashes, as the server spelled it.
	Path string
	// Size is the size the server reports. It is not validated.
	Size int64
	// ModTime is the server's last-write time.
	ModTime time.Time
	Kind    Kind
}

// Session is an open, read-only view of the share. Its methods may be called
// concurrently.
//
// A call whose ctx is canceled may not return until the session ends: the SMB
// implementation waits for a request already sent, so a server that stops
// answering holds the call until the session's own context (the one given to
// Dial) is canceled or Close is called. Close always ends every pending call.
type Session interface {
	// Walk lists the tree under source recursively, without the source
	// itself. Each directory is followed by its contents, and the entries of
	// one directory are in name order. Symbolic links are listed as Other
	// and not followed. A missing source returns an error wrapping
	// ErrSourceNotFound; any other failure is returned as it is, with no
	// partial listing.
	Walk(ctx context.Context, source string) ([]Entry, error)
	// Open opens a file for reading only. ctx stays bound to the file: once
	// it is done, reads and Close fail with its error.
	Open(ctx context.Context, path string) (File, error)
	// Close ends the session and every pending call. Open files become
	// unusable.
	Close() error
}

// File is a remote file open for reading. WriteTo is the fast path: for the
// SMB implementation it keeps several reads in flight, and io.Copy uses it.
type File interface {
	io.Reader
	io.WriterTo
	io.Closer
}

// validPath reports whether p is a path the share would receive exactly as
// given: a valid io/fs path other than ".", with no backslash (the SMB
// separator).
func validPath(p string) bool {
	return p != "." && fs.ValidPath(p) && !strings.Contains(p, `\`)
}

// validName reports whether a listed name is one path element, so joining
// it to its directory cannot clean it into another path.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.Contains(name, "/")
}

func kindOf(mode fs.FileMode) Kind {
	switch {
	case mode.IsRegular():
		return Regular
	case mode.IsDir():
		return Dir
	default:
		return Other
	}
}

// walk lists source in fsys for Session.Walk.
func walk(ctx context.Context, fsys fs.FS, source string) ([]Entry, error) {
	if !validPath(source) {
		return nil, &fs.PathError{Op: "walk", Path: source, Err: fs.ErrInvalid}
	}
	var entries []Entry
	// The root is the first call. Telling it apart by path would not do: a
	// listed name of "" or "." joins back to the source itself.
	atRoot := true
	err := fs.WalkDir(fsys, source, func(p string, d fs.DirEntry, err error) error {
		if atRoot {
			atRoot = false
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("%w: %s: %w", ErrSourceNotFound, source, err)
				}
				return err
			}
			if !d.IsDir() {
				return fmt.Errorf("source %s is not a directory", source)
			}
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// fs.WalkDir joins names with path.Join, which would clean a name
		// such as ".." or "a/b" into a different path.
		if !validName(d.Name()) {
			return fmt.Errorf("listing %s: invalid name %q", source, d.Name())
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, Entry{Path: p, Size: info.Size(), ModTime: info.ModTime(), Kind: kindOf(info.Mode())})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}
