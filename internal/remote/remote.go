// Package remote is a view of the telescope's SMB share.
//
// Session is read-only: it lists the share and opens files for reading, and
// nothing else. It has no call that creates, changes, renames, deletes or
// locks anything on the share. Dial returns the go-smb2 implementation;
// NewFake returns one backed by an io/fs.FS for tests. Both list with
// fs.WalkDir through the same code, so the listing rules below hold for both.
//
// Remover, used only by the delete command, removes a regular file or an
// empty folder by path and nothing else. go-smb2 may clear a read-only
// attribute in order to do it. Only DialRemover returns one for the share,
// and NewFakeDir one over a local folder for tests.
//
// Paths are share-relative, slash-separated and kept byte for byte. A path
// handed to Open, ReadDir (other than "." for the root), RemoveFile or
// RemoveDir must pass ValidPath; any other path is refused with
// fs.ErrInvalid rather than cleaned.
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

// ErrNotDir is returned, wrapped, by ReadDir for a path that is not a folder.
var ErrNotDir = errors.New("not a folder")

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

// Entry is one listed file or directory of the share.
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
	// Walk lists the whole share recursively, without the root itself.
	// Each directory is followed by its contents, and the entries of one
	// directory are in name order. Symbolic links are listed as Other and
	// not followed. Any failure is returned as it is, with no partial
	// listing.
	//
	// skip, if not nil, is called with the path of every listed entry, and
	// whether it is a directory, before a directory's contents are read.
	// An entry for which it returns true is left out, and so is everything
	// below it: that directory is never read.
	Walk(ctx context.Context, skip func(path string, dir bool) bool) ([]Entry, error)
	// ReadDir lists one folder, dir, which is "." for the root or a path
	// ValidPath accepts; any other is refused with fs.ErrInvalid before
	// any request. Its entries are in name order, with the listing rules
	// of Walk. A path that is not a folder gives an error matching
	// ErrNotDir.
	ReadDir(ctx context.Context, dir string) ([]Entry, error)
	// Open opens a file for reading only. ctx stays bound to the file: once
	// it is done, reads and Close fail with its error.
	Open(ctx context.Context, path string) (File, error)
	// Close ends the session and every pending call. Open files become
	// unusable.
	Close() error
}

// File is a remote file open for reading. WriteTo is the fast path: for the
// SMB implementation it keeps several reads in flight, and io.Copy uses it.
// Seek and Stat use the context the file was opened with.
type File interface {
	io.Reader
	io.Seeker
	io.WriterTo
	io.Closer
	Stat() (fs.FileInfo, error)
}

// ValidPath reports whether p is a path the share would receive exactly as
// given: a valid io/fs path other than ".", with no backslash (the SMB
// separator) and no NUL. Open, ReadDir, RemoveFile and RemoveDir refuse any
// other path.
func ValidPath(p string) bool {
	return p != "." && fs.ValidPath(p) && !strings.ContainsAny(p, `\`+"\x00")
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

// walk lists the root of fsys for Session.Walk.
func walk(ctx context.Context, fsys fs.FS, skip func(string, bool) bool) ([]Entry, error) {
	var entries []Entry
	// The root is the first call. Telling it apart by path would not do: a
	// listed name of "" or "." joins back to the root itself.
	atRoot := true
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if atRoot {
			atRoot = false
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return errors.New("share root is not a directory")
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
			return fmt.Errorf("listing the share: invalid name %q", d.Name())
		}
		// fs.WalkDir reads a directory only after this returns, so a
		// skipped one is never read.
		if skip != nil && skip(p, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
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

// readDir lists the folder dir of fsys for Session.ReadDir. dir has passed
// validDir.
func readDir(fsys fs.FS, dir string) ([]Entry, error) {
	des, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(des))
	for _, d := range des {
		// Joining a name such as ".." or "a/b" would name another path.
		if !validName(d.Name()) {
			return nil, fmt.Errorf("listing %q: invalid name %q", dir, d.Name())
		}
		info, err := d.Info()
		if err != nil {
			return nil, err
		}
		p := d.Name()
		if dir != "." {
			p = dir + "/" + p
		}
		entries = append(entries, Entry{Path: p, Size: info.Size(), ModTime: info.ModTime(), Kind: kindOf(info.Mode())})
	}
	return entries, nil
}

// validDir reports whether dir may be given to ReadDir: the root, ".", or
// a path ValidPath accepts.
func validDir(dir string) bool { return dir == "." || ValidPath(dir) }
