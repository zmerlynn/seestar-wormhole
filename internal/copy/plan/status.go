package plan

// File statuses against a destination's state, as find and browse show them.
const (
	StatusCopied  = "copied"  // recorded with the listed size and mtime
	StatusChanged = "changed" // recorded with another size or mtime
	StatusNew     = "new"     // not recorded
)

// FileStatus compares a listed file with its record, as a sync pass does:
// the exact size and the same instant, with no adoption tolerance. recorded
// says whether rec is a record at all.
func FileStatus(e Entry, rec Recorded, recorded bool) string {
	switch {
	case !recorded:
		return StatusNew
	case rec.Size == e.Size && rec.ModTime.Equal(e.ModTime):
		return StatusCopied
	}
	return StatusChanged
}
