package engine

import (
	"github.com/zmerlynn/seestar-copy/internal/copier"
	"github.com/zmerlynn/seestar-copy/internal/plan"
)

// Hooks are calls the engine makes as it works, for a caller that shows
// progress. Each is optional: a nil func is never called, and the CLI sets
// none. A hook runs on the engine's goroutines and should return quickly.
type Hooks struct {
	// Listed is called while a sync pass or a delete run lists the share,
	// once for each folder it will read, before reading it, with its path
	// and the number of folders so far.
	Listed func(folder string, folders int)
	// ToCopy is called while a sync pass or a dry run lists the share:
	// for the first entry listed, then at most once a second, or less
	// often on a share large enough that deciding takes a while. It gets
	// the files the pass would download (copies and replacements) and
	// their listed bytes, decided by the pass's rules on what is listed so
	// far, at that time. The count can drop: a folder listed later can
	// make a name clash with files already counted. Planned has the
	// pass's own total.
	ToCopy func(files int, bytes int64)
	// Decided is called once by a sync dry run that listed the share,
	// with every listed file's decision, in path order, and what the
	// selection left out. A real pass never calls it.
	Decided func([]plan.Decision, LeftOut)
	// Planned is called after a sync pass's decisions, with the files it
	// will download (copies and replacements) and their listed bytes.
	Planned func(files int, bytes int64)
	// Copying is [copier.Copier.Copying] for a sync pass's downloads.
	// Calls for different files can overlap.
	Copying func(path string, done, size int64)
	// Done is called with the result of each file a sync pass copies,
	// replaces or adopts, or fails to, as it finishes. Calls never overlap.
	Done func(copier.Result)
	// PassDone is called with the summary of every sync pass, however it
	// ended, after its pass line is logged.
	PassDone func(Summary)
	// Checked is called once in a delete run, after the listing, with the
	// number of listed files it will decide on.
	Checked func(files int)
	// Hashing is called in a delete run before each local copy is checked
	// against its SHA-256, with the file's path.
	Hashing func(path string)
	// Hashed is called in a delete run after each local copy is checked
	// against its SHA-256, with the bytes read so far and the bytes all
	// such checks of the run plan to read: the listed sizes of the files
	// to check. A copy that is missing, of another size or unreadable is
	// read in part or not at all, so a run can finish below that total.
	Hashed func(done, total int64)
	// Deleted is called after each file a delete run removed from the
	// device, with its listed size. A dry run removes nothing and never
	// calls it.
	Deleted func(path string, size int64)
}
