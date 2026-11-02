package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// Append checks rec, writes it as one line, fsyncs the file, and only then
// makes it the record for rec.Path. Times are written in UTC with their
// nanoseconds. The path must be a non-empty, valid UTF-8, share-relative path
// with forward slashes and no "." or ".." elements; the size must be zero or
// more; the mtime and at must be set; the SHA-256 must be 64 lowercase hex
// digits; how must be Copied, Replaced or Adopted. On any error the records
// are unchanged. If a failed line cannot be cut back off the file and the cut
// fsynced, every later Append fails.
func (s *State) Append(rec Record) error {
	readonly.Check("state.Append")
	if err := checkAppend(rec); err != nil {
		return fmt.Errorf("refuse state record for %q: %w", rec.Path, err)
	}
	rec.MTime = rec.MTime.UTC()
	rec.At = rec.At.UTC()
	line, err := encodeLine(rec)
	if err != nil {
		return fmt.Errorf("encode state record for %q: %w", rec.Path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.f == nil:
		return errors.New("state file is closed")
	case s.broken != nil:
		return fmt.Errorf("state file %s cannot be appended to after an earlier failure: %w", s.path, s.broken)
	}
	_, err = s.f.WriteAt(line, s.end)
	if err == nil {
		err = syncFile(s.f)
	}
	if err != nil {
		// Cut off what may have reached the file, durably, so it can
		// neither glue onto the next line nor load as a record after a
		// crash. If that fails, appending more could leave a bad line in the
		// middle; stop instead.
		uerr := s.f.Truncate(s.end)
		if uerr == nil {
			uerr = syncFile(s.f)
		}
		if uerr != nil {
			s.broken = uerr
			return fmt.Errorf("append to state file %s: %w; undoing it failed too: %w", s.path, err, uerr)
		}
		return fmt.Errorf("append to state file %s: %w", s.path, err)
	}
	s.end += int64(len(line))
	s.records[rec.Path] = rec
	return nil
}

// checkAppend holds a new record to everything this release writes.
func checkAppend(rec Record) error {
	if err := checkLoaded(rec); err != nil {
		return err
	}
	if b, err := hex.DecodeString(rec.SHA256); err != nil || len(b) != sha256.Size || strings.ToLower(rec.SHA256) != rec.SHA256 {
		return fmt.Errorf("sha256 %q is not 64 lowercase hex digits", rec.SHA256)
	}
	if rec.At.IsZero() {
		return errors.New("at is missing")
	}
	if !knownHow(rec.How) {
		return fmt.Errorf("how %q is not copied, replaced or adopted", rec.How)
	}
	return nil
}
