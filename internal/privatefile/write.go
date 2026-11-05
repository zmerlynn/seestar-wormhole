package privatefile

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Fault-injection seams for tests.
var (
	syncFile = (*os.File).Sync
	replace  = Replace
)

// WriteFile atomically replaces name in dir, an open directory, with a private
// file holding b. It writes b to a new temporary file next to name, created
// exclusively under a random name so no existing file or link is opened,
// fsyncs and closes it, replaces name with it, and syncs dir. A crash leaves
// the old file or the whole new one, and at worst a stray temporary file. The
// new file is 0600 whatever the umask; on Windows it has the protected DACL
// [Check] accepts, written through, and Replace keeps readers of the old file
// working. The caller decides whether an existing file may be replaced.
func WriteFile(dir *os.File, name string, b []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := filepath.Join(dir.Name(), "."+name+"."+hex.EncodeToString(nonce[:])+".tmp")
	f, err := create(tmp)
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
	if err := replace(tmp, filepath.Join(dir.Name(), name)); err != nil {
		return err
	}
	return SyncDirectory(dir)
}
