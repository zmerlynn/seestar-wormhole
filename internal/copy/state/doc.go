// Package state keeps sync's record of what it has copied into one
// destination.
//
// The state of a destination is a JSON Lines file at [Path], outside the
// destination. Its first line is a header, {"v":1,"dest":"/abs/dest"}. Every
// later line is one [Record]. The caller appends a record only after the file
// it describes is complete under its final name, so the state never names a
// file that is not on disk. When a path has several records, the last one
// wins.
//
// [Open] takes an exclusive, non-blocking OS lock on a lock file next to the
// state file (flock on Unix, LockFileEx on Windows). A second Open of the same
// destination, in this process or another, fails with [ErrLocked] until the
// first is closed or its process exits. The lock file is never removed.
//
// The state is keyed by the exact spelling of the destination. So that a
// symbolic link or another spelling of the same folder cannot start a second
// history with a second lock, Open compares the destination with those named
// by the other state files' headers, using [os.SameFile], under a short
// registration lock (sync/registry.lock, also never removed), and refuses a
// folder that is already registered under another spelling. This is best
// effort: a destination that is gone or unreadable cannot be compared, and a
// different state directory is never consulted. Use one spelling per
// destination. Open also refuses a state directory that is, or resolves to a
// place, inside the destination, or that has an existing ancestor that is the
// destination's folder by [os.SameFile]: another case on a case-insensitive
// filesystem, or a bind mount of the whole destination.
//
// Trust and filesystem requirements: the state directory and all of its
// ancestors must be trusted, and its sync directory must be private to the
// user. Anyone who can write there can change or redirect the state, or
// replace a lock file and so defeat the lock. Open creates the sync directory
// 0700 and new files 0600, but keeps the permissions of anything that already
// exists. On Windows a new state file gets a protected DACL for the user and
// SYSTEM; the sync directory and lock files keep the ACLs they inherit. The
// state must be on a local filesystem with reliable OS locks and fsync.
// Shared and network state directories are not supported.
//
// Crash semantics:
//   - A new state file is written whole to a new temporary file, fsynced,
//     closed and renamed into place, and the directory is fsynced (Windows
//     writes it through and replaces it with ReplaceFile instead), so a state
//     file either does not exist or starts with a complete header.
//   - [State.Append] writes one line and fsyncs it before the record counts.
//     A failed line is cut off again and the cut fsynced. A crash leaves at
//     most a torn final line. Open drops a final line that
//     has no newline or does not hold a valid record, logs a warning, and
//     truncates the file to the end of the last good line, so the next record
//     starts on a fresh line.
//   - Any other bad line, a line longer than 1 MiB, and a header with another
//     version or destination are fatal: Open refuses the file and does not
//     change it.
//
// Reading is permissive: unknown members are ignored, times may carry any
// RFC 3339 offset, and an unknown how value is kept as it is. A loaded record
// needs only a valid path, a size of zero or more, and an mtime. Writing is
// strict; see [State.Append].
package state
