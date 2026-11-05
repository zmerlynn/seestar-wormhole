package privatefile

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

// A Policy says how [WriteFile] protects the file it publishes. The steps that
// make the write atomic and durable are the same under both.
type Policy int

const (
	// Private makes the file private as [Check] requires: 0600 on Unix and, on
	// Windows, a protected DACL for the user and SYSTEM, which needs a
	// filesystem with ACLs, placed with [Replace].
	Private Policy = iota
	// Inherited is Private on Unix. On Windows the file keeps the ACL its
	// folder passes down and is renamed into place, as [os.Rename] does, so
	// it also works on filesystems without ACLs such as FAT32 and exFAT.
	Inherited
)

// Fault-injection seams for tests.
var (
	syncFile = (*os.File).Sync
	replace  = publish
)

// WriteFile atomically replaces name in dir, an open directory, with a file
// holding b, protected as p says. It writes b to a new temporary file next to
// name, created exclusively under a random name so no existing file or link is
// opened, fsyncs and closes it, replaces name with it, and syncs dir. A crash
// leaves the old file or the whole new one, and at worst a stray temporary
// file. On Unix the new file is 0600 whatever the umask and owned by the user;
// on Windows it is written through. The caller decides whether an existing
// file may be replaced.
func WriteFile(dir *os.File, name string, b []byte, p Policy) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := filepath.Join(dir.Name(), "."+name+"."+hex.EncodeToString(nonce[:])+".tmp")
	f, err := create(tmp, p)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = syncFile(f)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := replace(tmp, filepath.Join(dir.Name(), name), p); err != nil {
		return err
	}
	return SyncDirectory(dir)
}
