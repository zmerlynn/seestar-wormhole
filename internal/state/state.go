package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

const (
	// version is the only header version this release reads and writes.
	version = 1
	// maxLine bounds one line of the state file, newline included. A record
	// is about 250 bytes plus its path, so only an absurd path comes near it.
	maxLine = 1 << 20
)

// How says how a record's file came to be in the destination.
type How string

// The how values this release writes. A loaded record may carry others.
const (
	Copied   How = "copied"   // downloaded; not in the state before
	Replaced How = "replaced" // downloaded again because the device file changed
	Adopted  How = "adopted"  // already in the destination; hashed, not downloaded
)

// Record is one line of the state file: a file of the share that is complete
// in the destination.
type Record struct {
	Path   string    `json:"path"`   // share-relative, forward slashes
	Size   int64     `json:"size"`   // bytes, as listed by the device
	MTime  time.Time `json:"mtime"`  // the device's mtime, compared with Equal
	SHA256 string    `json:"sha256"` // lowercase hex of the local file's SHA-256
	At     time.Time `json:"at"`     // when the record was made
	How    How       `json:"how"`
}

type header struct {
	V    int    `json:"v"`
	Dest string `json:"dest"`
}

// Fault-injection seams for tests. syncFile makes a file's or directory's
// contents durable; rename publishes a new state file; registerHook, if set,
// runs under the registration lock between the alias check and creating the
// state file.
var (
	syncFile     = (*os.File).Sync
	rename       = os.Rename
	registerHook func()
)

// State is an open, locked state file. Its methods are safe for concurrent
// use.
type State struct {
	path string
	lock *os.File

	// mu guards the fields below and serializes writes to f, so each record
	// reaches the file as one whole line at end.
	mu      sync.Mutex
	f       *os.File // nil once closed
	end     int64    // the end of the last good line; the next line goes here
	broken  error    // set when a failed append could not be undone
	records map[string]Record
}

// Open locks and loads the state of dest, creating dest, the sync directory
// (0700) and the state file (0600) if they are missing. It logs a dropped torn
// line to log at warn level and unknown how values at debug; a nil log means
// slog.Default(). A held lock fails with an error wrapping [ErrLocked].
//
// Open refuses a state directory that is inside dest, by its path, by where
// its symbolic links lead, or because one of its existing ancestors is the
// same folder as dest under another spelling, such as another case on a
// case-insensitive filesystem or a bind mount of dest. It also refuses dest if another state file in the
// sync directory names the same folder under another spelling, such as
// through a symbolic link; that check is best effort, so use one spelling per
// destination.
//
// stateDir and its ancestors must be trusted, its sync directory must be
// private to the user, and it must be on a local filesystem with reliable OS
// locks and fsync; shared and network state directories are not supported.
// Open keeps the permissions of a directory or file that already exists; on
// Windows, privacy comes from inherited ACLs.
func Open(stateDir, dest string, log *slog.Logger) (*State, error) {
	readonly.Check("state.Open")
	if log == nil {
		log = slog.Default()
	}
	path, absDest, err := resolve(stateDir, dest)
	if err != nil {
		return nil, err
	}
	if !utf8.ValidString(absDest) {
		// The header could not record it byte for byte.
		return nil, fmt.Errorf("destination %q is not valid UTF-8", absDest)
	}
	// The destination must exist to compare it with the state directory.
	if err := os.MkdirAll(absDest, 0o777); err != nil {
		return nil, fmt.Errorf("create destination: %w", err)
	}
	syncDir := filepath.Dir(path)
	if err := checkOutside(absDest, filepath.Dir(syncDir), syncDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(syncDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	lockPath := strings.TrimSuffix(path, ".jsonl") + ".lock"
	lock, err := lockFile(lockPath, false)
	if errors.Is(err, ErrLocked) {
		return nil, fmt.Errorf("destination %s: %w (lock file %s)", absDest, ErrLocked, lockPath)
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s for destination %s: %w", lockPath, absDest, err)
	}
	s, err := openLocked(path, absDest, log)
	if err != nil {
		lock.Close()
		return nil, err
	}
	s.lock = lock
	return s, nil
}

// openLocked loads the state file at path, which the caller has locked.
func openLocked(path, absDest string, log *slog.Logger) (*State, error) {
	readonly.Check("state.openLocked")
	if err := register(path, absDest, log); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open state file: %w", err)
	}
	s := &State{path: path, f: f}
	var torn *badLine
	s.records, s.end, torn, err = load(f, absDest, log)
	if err == nil && torn != nil {
		log.Warn("dropped the torn final line of the state file", "file", path, "line", torn.n, "reason", torn.err)
		if err = f.Truncate(s.end); err == nil {
			err = syncFile(f)
		}
		if err != nil {
			err = fmt.Errorf("truncate the torn final line: %w", err)
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("state file %s: %w", path, err)
	}
	return s, nil
}

// registryName is the lock file in the sync directory that serializes
// registering destinations. Like the other lock files, it is never removed.
const registryName = "registry.lock"

// register checks that no other state file in the sync directory names the
// folder absDest names, and creates the state file at path if it is missing
// or empty. It holds the registration lock from the check through the
// creation, so two first opens of one folder under different spellings cannot
// both pass. It checks even when the state file exists, to find a history
// that is already split.
func register(path, absDest string, log *slog.Logger) error {
	readonly.Check("state.register")
	regPath := filepath.Join(filepath.Dir(path), registryName)
	reg, err := lockFile(regPath, true)
	if err != nil {
		return fmt.Errorf("lock %s for destination %s: %w", regPath, absDest, err)
	}
	defer reg.Close()
	if err := checkAliases(path, absDest, log); err != nil {
		return err
	}
	if registerHook != nil {
		registerHook()
	}
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || err == nil && fi.Size() == 0 {
		err = create(path, absDest)
	}
	if err != nil {
		return fmt.Errorf("create state file %s: %w", path, err)
	}
	return nil
}

// checkAliases refuses absDest if the header of another state file next to
// path names the same folder (by [os.SameFile]) under another spelling. It is
// best effort: a state whose destination is missing or cannot be read is
// skipped, at debug level, and a malformed state file is skipped with a
// warning, since it belongs to another destination. It never changes another
// state file.
func checkAliases(path, absDest string, log *slog.Logger) error {
	destInfo, err := os.Stat(absDest)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	dir := filepath.Dir(path)
	des, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list state directory: %w", err)
	}
	for _, de := range des {
		name := de.Name()
		if name == filepath.Base(path) || !strings.HasSuffix(name, ".jsonl") || de.IsDir() {
			continue
		}
		other := filepath.Join(dir, name)
		h, err := readHeader(other)
		if err != nil {
			log.Warn("skipped a malformed state file while checking for another spelling of the destination", "file", other, "reason", err)
			continue
		}
		fi, err := os.Stat(h.Dest)
		if err != nil {
			log.Debug("skipped a state file whose destination cannot be found", "file", other, "dest", h.Dest, "reason", err)
			continue
		}
		if os.SameFile(destInfo, fi) {
			return fmt.Errorf("destination %s is the same folder as %s, which already has the state file %s; use one spelling per destination", absDest, h.Dest, other)
		}
	}
	return nil
}

// readHeader reads the header of the state file at p.
func readHeader(p string) (header, error) {
	f, err := os.Open(p)
	if err != nil {
		return header{}, err
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, maxLine).ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return header{}, fmt.Errorf("line 1 is longer than %d bytes", maxLine)
	case errors.Is(err, io.EOF):
		return header{}, errors.New("the header has no newline")
	case err != nil:
		return header{}, err
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return header{}, fmt.Errorf("not a state header: %w", err)
	}
	if h.V == 0 || !filepath.IsAbs(h.Dest) {
		return header{}, errors.New("not a state header: no version or no absolute destination")
	}
	return h, nil
}

// create writes a state file holding only the header for absDest, through a
// temporary file so that a crash cannot leave a partial header. The
// temporary file is new, created exclusively under a random name, so an
// existing file or link in the sync directory is never opened, truncated or
// published.
func create(path, absDest string) (err error) {
	readonly.Check("state.create")
	line, err := encodeLine(header{V: version, Dest: absDest})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(line)
	}
	if err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := rename(tmp, path); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	// The sync directory may be new too.
	return syncDir(filepath.Dir(dir))
}

// syncDir makes the entries of dir durable where the OS supports it.
func syncDir(dir string) error {
	readonly.Check("state.syncDir")
	if runtime.GOOS == "windows" {
		// Windows cannot flush a directory opened with os.Open; NTFS
		// journals the rename itself.
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = syncFile(d)
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// Load reads the records of dest's state for a dry run, and changes nothing on
// disk. It creates no folder or file, takes no lock and repairs nothing: a
// torn final line is left in place, logged at warn level, and its record
// ignored, as [Open] would drop it. A missing state directory or state file,
// or an empty state file, gives no records. A missing destination is left
// absent; its existing state records are still loaded. Like Open, Load
// refuses a state directory inside dest and, when the sync directory exists,
// another spelling of a destination that already has a state file; it
// refuses a state file it cannot parse for the same reasons Open does.
func Load(stateDir, dest string, log *slog.Logger) (map[string]Record, error) {
	if log == nil {
		log = slog.Default()
	}
	path, absDest, err := resolve(stateDir, dest)
	if err != nil {
		return nil, err
	}
	if !utf8.ValidString(absDest) {
		return nil, fmt.Errorf("destination %q is not valid UTF-8", absDest)
	}
	syncDir := filepath.Dir(path)
	switch _, err := os.Stat(absDest); {
	case errors.Is(err, fs.ErrNotExist):
		// Open would create it empty, so no alias or nesting check could
		// fail on it.
	case err != nil:
		return nil, fmt.Errorf("destination: %w", err)
	default:
		if err := checkOutside(absDest, filepath.Dir(syncDir), syncDir); err != nil {
			return nil, err
		}
		if _, err := os.Stat(syncDir); err == nil {
			if err := checkAliases(path, absDest, log); err != nil {
				return nil, err
			}
		}
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open state file: %w", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, fmt.Errorf("state file %s: %w", path, err)
	} else if fi.Size() == 0 {
		// Open would write a new header over it.
		return map[string]Record{}, nil
	}
	records, _, torn, err := load(f, absDest, log)
	if err != nil {
		return nil, fmt.Errorf("state file %s: %w", path, err)
	}
	if torn != nil {
		log.Warn("ignored the torn final line of the state file", "file", path, "line", torn.n, "reason", torn.err)
	}
	return records, nil
}

// badLine is a line that did not hold a record. It is fatal unless it is the
// final line.
type badLine struct {
	n   int // 1-based line number
	err error
}

// load reads a state file for absDest from r. It returns the last record per
// path, the end of the last good line, and the final line if it was dropped
// as torn.
func load(r io.Reader, absDest string, log *slog.Logger) (records map[string]Record, end int64, torn *badLine, err error) {
	records = make(map[string]Record)
	br := bufio.NewReaderSize(r, maxLine)
	n := 0
	for {
		line, rerr := br.ReadSlice('\n')
		if errors.Is(rerr, bufio.ErrBufferFull) {
			return nil, 0, nil, fmt.Errorf("line %d is longer than %d bytes", n+1, maxLine)
		}
		if rerr != nil && rerr != io.EOF {
			return nil, 0, nil, fmt.Errorf("read: %w", rerr)
		}
		if len(line) == 0 {
			break
		}
		n++
		if torn != nil {
			// A bad line with more after it is not a torn tail.
			return nil, 0, nil, fmt.Errorf("line %d: %w", torn.n, torn.err)
		}
		complete := line[len(line)-1] == '\n'
		if n == 1 {
			if !complete {
				return nil, 0, nil, errors.New("line 1: the header has no newline")
			}
			if err := checkHeader(line, absDest); err != nil {
				return nil, 0, nil, fmt.Errorf("line 1: %w", err)
			}
			end += int64(len(line))
			continue
		}
		if !complete {
			torn = &badLine{n, errors.New("no newline at the end of the file")}
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			torn = &badLine{n, err}
			continue
		}
		if err := checkLoaded(rec); err != nil {
			torn = &badLine{n, fmt.Errorf("invalid record: %w", err)}
			continue
		}
		if !knownHow(rec.How) {
			log.Debug("state record has an unknown how value", "line", n, "path", rec.Path, "how", rec.How)
		}
		records[rec.Path] = rec
		end += int64(len(line))
	}
	if n == 0 {
		return nil, 0, nil, errors.New("the file has no header")
	}
	return records, end, torn, nil
}

func checkHeader(line []byte, absDest string) error {
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return fmt.Errorf("not a state header: %w", err)
	}
	switch {
	case h.V == 0:
		return errors.New("not a state header: no version")
	case h.V != version:
		return fmt.Errorf("state version %d is not supported; this release reads version %d", h.V, version)
	case h.Dest != absDest:
		return fmt.Errorf("the state is for destination %q, not %q", h.Dest, absDest)
	}
	return nil
}

// Lookup returns the last record for path.
func (s *State) Lookup(path string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[path]
	return rec, ok
}

// Snapshot returns a copy of the last record for every path.
func (s *State) Snapshot() map[string]Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]Record, len(s.records))
	for k, v := range s.records {
		m[k] = v
	}
	return m
}

// Close closes the state file and releases the lock. Closing twice is a
// no-op.
func (s *State) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	if lerr := s.lock.Close(); err == nil {
		err = lerr
	}
	return err
}

// encodeLine encodes v as one JSON line. HTML characters are left as they
// are, so paths stay readable.
func encodeLine(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if b.Len() > maxLine {
		return nil, fmt.Errorf("line is %d bytes, more than %d", b.Len(), maxLine)
	}
	return b.Bytes(), nil
}

// checkLoaded holds a loaded record to what the pass decisions rely on.
func checkLoaded(rec Record) error {
	if err := checkPath(rec.Path); err != nil {
		return err
	}
	if rec.Size < 0 {
		return fmt.Errorf("size %d is negative", rec.Size)
	}
	if rec.MTime.IsZero() {
		return errors.New("mtime is missing")
	}
	return nil
}

func checkPath(p string) error {
	switch {
	case p == "":
		return errors.New("path is empty")
	case !utf8.ValidString(p):
		return fmt.Errorf("path %q is not valid UTF-8", p)
	case strings.Contains(p, `\`):
		return fmt.Errorf("path %q contains a backslash", p)
	case p == "." || !fs.ValidPath(p):
		return fmt.Errorf("path %q is not relative with forward slashes and no empty, . or .. elements", p)
	}
	return nil
}

func knownHow(h How) bool {
	return h == Copied || h == Replaced || h == Adopted
}
