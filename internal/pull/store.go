package pull

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

const metadata = ".seestar-wormhole"
const sourceRoot = "EMMC Images/MyWorks"
const shareRoot = "EMMC Images"
const directoryStateVersion = 5
const maxStateBytes = 64 << 20
const maxEntries = 200000
const stateVersion = 4

const checkSize = "size"
const checkReread = "reread"

// ErrLocked means another process owns this destination.
var ErrLocked = failure(diagnostic.LockHeld)

// Receipt records the check performed against stable source metadata and the
// SHA-256 of the local bytes written. Size checks do not attest byte equality.
type Receipt struct {
	Source     string    `json:"source"`
	Generation string    `json:"generation"`
	Size       int64     `json:"size"`
	Mtime      time.Time `json:"mtime"`
	SHA256     string    `json:"sha256"`
	Check      string    `json:"check"`
}

// Selection isolates auto checkpoints for a normalized folder filter set.
type Selection struct {
	Dir        string    `json:"dir,omitempty"`
	Only       []string  `json:"only"`
	Checkpoint time.Time `json:"checkpoint"`
}

// State binds one destination to an opaque device identity and inventories all
// verified files across selections. Credentials and installation UUIDs are absent.
type State struct {
	Version    int                  `json:"version"`
	Generation string               `json:"generation"`
	Source     string               `json:"source"`
	Root       string               `json:"root"`
	Selections map[string]Selection `json:"selections"`
	Files      map[string]Receipt   `json:"files"`
}

// Destination holds the OS lock until Close. The lock file persists but the OS
// releases its lock on process exit; unlinking it would permit split ownership.
type Destination struct {
	path, dir       string
	lock, directory *os.File
	State           State
	readOnly        bool
}

// OpenDestination locks before cloud login or device acquisition. Data directory
// creation respects umask; metadata uses private permissions or a private ACL.
func OpenDestination(dest string) (*Destination, error) {
	absolute, err := filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0777); err != nil {
		return nil, err
	}
	if err = checkLocalPath(absolute); err != nil {
		return nil, err
	}
	d := &Destination{path: absolute, dir: filepath.Join(absolute, metadata)}
	d.directory, err = privateDirectory(d.dir)
	if err != nil {
		return nil, err
	}
	if err = checkLocalPath(filepath.Join(d.dir, "lock")); err != nil {
		d.Close()
		return nil, err
	}
	d.lock, err = privatefile.Open(filepath.Join(d.dir, "lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		d.Close()
		return nil, err
	}
	if err = checkPrivate(d.lock); err == nil {
		err = lockFile(d.lock)
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	if err = d.load(); err != nil {
		d.Close()
		return nil, err
	}
	if err = d.cleanPartials(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// OpenReadOnlyDestination reads receipts and checkpoints without creating a
// directory, acquiring a writer lock, changing permissions or cleaning partials.
// Its snapshot is advisory when a writer is running concurrently.
func OpenReadOnlyDestination(dest string) (*Destination, error) {
	absolute, err := filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
	if err = checkLocalPath(filepath.Join(absolute, metadata)); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(absolute); err == nil {
		if !info.IsDir() {
			return nil, failure(diagnostic.UnsafePath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	d := &Destination{path: absolute, dir: filepath.Join(absolute, metadata), readOnly: true}
	if err = d.load(); err != nil {
		return nil, err
	}
	return d, nil
}

// Close releases handles; it is safe on partially initialized destinations.
func (d *Destination) Close() error {
	var err error
	if d.lock != nil {
		err = d.lock.Close()
		d.lock = nil
	}
	if d.directory != nil {
		err = errors.Join(err, d.directory.Close())
		d.directory = nil
	}
	return err
}

func (d *Destination) load() error {
	filename := filepath.Join(d.dir, "state.json")
	if _, err := os.Lstat(filename); errors.Is(err, os.ErrNotExist) {
		d.State = State{Version: stateVersion, Root: sourceRoot, Selections: map[string]Selection{}, Files: map[string]Receipt{}}
		return nil
	}
	if err := checkLocalPath(filename); err != nil {
		return err
	}
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = checkPrivate(f); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxStateBytes {
		return failure(diagnostic.MetadataLimit)
	}
	dec := jsontext.NewDecoder(io.LimitReader(f, maxStateBytes+1))
	if err = json.UnmarshalDecode(dec, &d.State); err != nil {
		return failure(diagnostic.StateInvalid)
	}
	if err = json.UnmarshalDecode(dec, new(any)); err != io.EOF {
		return failure(diagnostic.StateInvalid)
	}
	s := d.State
	if (s.Version < 1 || s.Version > directoryStateVersion) || s.Source == "" || (s.Root != sourceRoot && s.Root != shareRoot) || (s.Root == shareRoot) != (s.Version == directoryStateVersion) || s.Selections == nil || s.Files == nil {
		return failure(diagnostic.StateInvalid)
	}
	for key, sel := range s.Selections {
		only := slices.Clone(sel.Only)
		slices.Sort(only)
		only = slices.Compact(only)
		expectedKey := directorySelectionKey(sel.Dir, only)
		if (s.Root == sourceRoot && sel.Dir != "") || (s.Root == shareRoot && (sel.Dir == "" || validateDirectories([]string{sel.Dir}) != nil)) {
			return failure(diagnostic.StateInvalid)
		}
		if s.Version == 1 {
			expectedKey = legacySelectionKey(only)
		}
		if (s.Version >= 2 && validatePatterns(only) != nil) || !slices.Equal(only, sel.Only) || key != expectedKey {
			return failure(diagnostic.StateInvalid)
		}
	}
	for path, r := range s.Files {
		raw, err := hex.DecodeString(r.SHA256)
		if s.Version >= stateVersion && r.Check != checkSize && r.Check != checkReread {
			return failure(diagnostic.StateInvalid)
		}
		if validateRemote(path) != nil || r.Size < 0 || r.Mtime.IsZero() || err != nil || len(raw) != 32 {
			return failure(diagnostic.StateInvalid)
		}
	}
	// Version 1 used globs. Retain verified bytes but retire its checkpoints:
	// the same text can select different folders under regexp semantics.
	if s.Version == 1 {
		d.State.Selections = map[string]Selection{}
	}
	if s.Version < 3 {
		// Legacy state cannot prove which binding verified any receipt. Even
		// fields supplied under an older schema cannot establish provenance.
		for path, receipt := range d.State.Files {
			receipt.Source, receipt.Generation = "", ""
			d.State.Files[path] = receipt
		}
	}
	// All prior schemas performed download comparison; version 3 already
	// proves attribution. Older schemas still require repair as above.
	if s.Version < stateVersion {
		for path, receipt := range d.State.Files {
			receipt.Check = checkReread
			d.State.Files[path] = receipt
		}
	}
	if s.Root == sourceRoot {
		d.State.Version = stateVersion
	}
	if d.State.Generation == "" || s.Version < 3 {
		d.State.Generation, err = randomName()
		if err != nil {
			return err
		}
	}
	return nil
}

// SourceMismatchError reports only the kinds of the old and requested bindings.
type SourceMismatchError struct{ ExistingKind, RequestedKind string }

func (e *SourceMismatchError) Error() string {
	return "destination source mismatch: " + e.ExistingKind + " to " + e.RequestedKind + "; use --rebind to take over"
}
func (e *SourceMismatchError) Unwrap() error { return failure(diagnostic.SourceMismatch) }

func sourceKind(source string) string {
	kind, _, _ := strings.Cut(source, ":")
	if kind == "direct" || kind == "via" {
		return kind
	}
	return "unknown"
}

// RebindSource deliberately takes over a destination and retires checkpoints.
// Default mode persists immediately; explicit mode stages the change until Run
// finishes inventory and planning. Receipts retain their original attribution.
func (d *Destination) RebindSource(source string) error {
	if source == "" {
		return errors.New("missing source identity")
	}
	generation, err := randomName()
	if err != nil {
		return err
	}
	old := d.State
	d.State.Source = source
	d.State.Generation = generation
	d.State.Selections = map[string]Selection{}
	if d.State.Root == shareRoot && !d.readOnly {
		return nil
	}
	if err := d.save(); err != nil {
		d.State = old
		return err
	}
	return nil
}

// BindSource refuses reuse by another source. Source must be nonempty. The
// binding is staged in memory; Run persists explicit bindings before transfer.
func (d *Destination) BindSource(source string) error {
	if source == "" {
		return errors.New("missing device identity")
	}
	if d.State.Source != "" && d.State.Source != source {
		return &SourceMismatchError{ExistingKind: sourceKind(d.State.Source), RequestedKind: sourceKind(source)}
	}
	if d.State.Generation == "" {
		generation, err := randomName()
		if err != nil {
			return err
		}
		d.State.Generation = generation
	}
	d.State.Source = source
	return nil
}

// BindLayout refuses mixing flattened MyWorks and explicit share-relative paths.
// Source rebinding does not override the layout; receipts use destination paths.
func (d *Destination) BindLayout(dirs []string) error {
	root, version := sourceRoot, stateVersion
	if len(dirs) > 0 {
		root, version = shareRoot, directoryStateVersion
	}
	if d.State.Source != "" && d.State.Root != root {
		return failure(diagnostic.LayoutMismatch)
	}
	d.State.Root, d.State.Version = root, version
	return nil
}

func directorySelectionKey(dir string, only []string) string {
	if dir == "" {
		return selectionKey(only)
	}
	return dir + "/" + selectionKey(only)
}

func selectionKey(only []string) string {
	return "regexp:" + legacySelectionKey(only)
}

func legacySelectionKey(only []string) string {
	// Keep selection hashes stable: v1 escaped HTML/JS in patterns.
	b, _ := json.Marshal(only, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	// nil and an empty list represent the same all-folder selection.
	if len(only) == 0 {
		b = []byte("[]")
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func randomName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (d *Destination) save() error {
	if d.readOnly {
		return errors.New("cannot save read-only destination")
	}
	// Preserve the state file: sorted maps, null nil collections, HTML/JS escapes, and two-space indentation.
	b, err := json.Marshal(d.State, json.Deterministic(true), json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true), jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	if len(b)+1 > maxStateBytes {
		return failure(diagnostic.MetadataLimit)
	}
	return atomicPrivate(d.dir, "state.json", append(b, '\n'))
}

func atomicPrivate(dir, name string, b []byte) error {
	token, err := randomName()
	if err != nil {
		return err
	}
	temp := filepath.Join(dir, ".tmp-"+token)
	f, err := privatefile.Open(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(temp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDirectory(dir)
}

// checkLocalPath rejects symlinks/reparse points in all existing components.
// Parents are trusted; concurrent noncooperating filesystem writers are outside
// the destination-lock contract. No file is accessed before this check.
func checkLocalPath(filename string) error {
	for p := filename; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return failure(diagnostic.UnsafePath)
		}
		if err == nil {
			if err = checkReparse(p); err != nil {
				return err
			}
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}
	return nil
}

func (d *Destination) cleanPartials() error {
	count := 0
	return filepath.WalkDir(d.path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > maxEntries {
			return failure(diagnostic.InventoryLimit)
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		suffix := ""
		if _, token, ok := strings.Cut(name, ".seestar-part-"); ok {
			suffix = token
		}
		if strings.HasPrefix(name, ".seestar-sums-") {
			suffix = strings.TrimPrefix(name, ".seestar-sums-")
		}
		if filepath.Dir(path) == d.dir && strings.HasPrefix(name, ".tmp-") {
			suffix = strings.TrimPrefix(name, ".tmp-")
		}
		if len(suffix) != 32 {
			return nil
		}
		raw, decodeErr := hex.DecodeString(suffix)
		if decodeErr != nil || len(raw) != 16 {
			return nil
		}
		if !entry.Type().IsRegular() {
			return failure(diagnostic.UnsafePath)
		}
		return os.Remove(path)
	})
}
