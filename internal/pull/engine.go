package pull

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/backend/smb"
	rfs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/walk"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"golang.org/x/text/unicode/norm"
	"unicode"
)

// Result describes a handled pull outcome. Planned counts files requiring
// copy or download comparison; Bytes is their listed size total in dry-run.
type Result struct {
	DiagnosticsLost            uint64
	Status                     string
	Reason, CleanupReason      string
	Copied, Verified, Deferred int
	Planned                    int
	Bytes                      int64
	Bounds                     Bounds
	FirstScan                  bool
}

// Batch retains the proposed checkpoint until the command confirms release.
type Batch struct {
	destination *Destination
	config      Config
	Result      Result
	selections  map[string]Selection
}

// Configure isolates supported rclone settings. No multistream splitting or
// destructive operations are enabled; verification shares the worker budget.
func Configure(ctx context.Context, c Config) context.Context {
	ctx = context.WithValue(ctx, shareRootKey{}, len(c.Dirs) > 0)
	ctx, ci := rfs.AddConfig(ctx)
	ci.Transfers = c.Transfers
	ci.Checkers = c.Transfers
	ci.MultiThreadStreams = 0
	ci.LowLevelRetries = 3
	ci.Inplace = true // all copies target our unique temporary filename
	ci.CheckSum = false
	ci.IgnoreChecksum = true // source has no checksum; hash only the local written bytes
	ci.NoUnicodeNormalization = true
	return ctx
}

// Run inventories all folders even outside the time window, then saves receipts
// as successful files finish. It never commits an auto checkpoint. Selection
// bounds use started; future-clock validation uses each metadata observation.
func (d *Destination) Run(ctx context.Context, c Config, started time.Time, src rfs.Fs) (batch *Batch, err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// A metadata method has no error return, and cancellation causes are first
	// writer wins. Retain its panic independently if another cancellation won.
	var panicked atomic.Bool
	boundary := panicBoundary{logger: c.PlanLogger, cancel: func(err error) {
		panicked.Store(true)
		cancel(err)
	}, abort: c.CancelOnPanic}
	src = guardedSource{Fs: src, boundary: boundary}
	err = Operation(c.PlanLogger, func() error {
		var runErr error
		batch, runErr = d.run(ctx, c, started, src)
		return runErr
	})
	if panicked.Load() {
		err = errors.Join(err, diagnostic.ErrPullPanic)
	}
	return batch, errors.Join(err, context.Cause(ctx))
}

func (d *Destination) run(ctx context.Context, c Config, started time.Time, src rfs.Fs) (*Batch, error) {
	if d.readOnly && !c.DryRun {
		return nil, errors.New("read-only destination requires dry-run")
	}
	if err := d.BindLayout(c.Dirs); err != nil {
		return nil, err
	}
	dirs := c.Dirs
	if len(dirs) == 0 {
		dirs = []string{""}
	}
	b := &Batch{destination: d, config: c, selections: map[string]Selection{}, Result: Result{Status: "error"}}
	windows := map[string]Bounds{}
	for i, dir := range dirs {
		key := directorySelectionKey(dir, c.Only)
		selection, exists := d.State.Selections[key]
		bounds, err := c.bounds(started, selection.Checkpoint)
		if err != nil {
			return b, err
		}
		windows[dir] = bounds
		b.selections[key] = Selection{Dir: dir, Only: slices.Clone(c.Only), Checkpoint: bounds.Upper}
		b.Result.FirstScan = b.Result.FirstScan || !exists
		// The summary encloses every per-directory window. A new directory makes
		// the aggregate lower bound unbounded; eligibility still uses its own bound.
		if i == 0 || bounds.Lower.Before(b.Result.Bounds.Lower) {
			b.Result.Bounds = bounds
		}
	}
	ctx = Configure(ctx, c)
	objects, directories, err := inventoryDirectories(ctx, src, c.Only, dirs)
	if err != nil {
		return b, err
	}
	if err = preflight(d.path, objects, directories...); err != nil {
		return b, err
	}
	var eligible []rfs.Object
	for _, o := range objects {
		dir := ""
		if len(c.Dirs) > 0 {
			dir = path.Dir(o.Remote())
			for {
				if _, ok := windows[dir]; ok {
					break
				}
				dir = path.Dir(dir)
			}
		}
		bounds := windows[dir]
		mtime := o.ModTime(ctx).UTC()
		// Metadata reads can block while the producer continues capturing. Only
		// the observation clock can distinguish new captures from clock skew.
		observed := time.Now().UTC()
		if mtime.After(observed) {
			return b, failure(diagnostic.FutureMtime)
		}
		if mtime.IsZero() || o.Size() < 0 {
			return b, failure(diagnostic.InvalidMetadata)
		}
		if mtime.After(bounds.Upper) {
			b.Result.Deferred++
			b.planLog(o, "defer")
			continue
		}
		receipt, known := d.State.Files[o.Remote()]
		localInfo, statErr := os.Lstat(filepath.Join(d.path, filepath.FromSlash(o.Remote())))
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return b, statErr
		}
		changed := !known || receipt.Source != d.State.Source || receipt.Generation == "" || receipt.Generation != d.State.Generation || receipt.Size != o.Size() || !receipt.Mtime.Equal(mtime) || errors.Is(statErr, os.ErrNotExist)
		if statErr == nil && (localInfo.Size() != receipt.Size) {
			changed = true
		}
		// A size receipt satisfies default repeats. --verify upgrades it even
		// below an auto checkpoint; --recheck forces comparison in the window.
		upgrade := c.Verify && receipt.Check != checkReread
		if !changed && !upgrade && !c.Recheck {
			b.planLog(o, "skip")
			continue
		}
		if !mtime.Before(bounds.Lower) || (c.Since == "auto" && (changed || upgrade)) {
			eligible = append(eligible, o)
			if c.DryRun {
				if o.Size() > math.MaxInt64-b.Result.Bytes {
					return b, failure(diagnostic.InvalidMetadata)
				}
				b.Result.Planned++
				b.Result.Bytes += o.Size()
				action := "copy"
				if statErr == nil && localInfo.Size() == o.Size() {
					action = "check"
				}
				b.planLog(o, action)
			}
		} else {
			b.planLog(o, "skip")
		}
	}
	if c.DryRun {
		b.Result.Status = "planned"
		return b, ctx.Err()
	}
	if len(c.Dirs) > 0 {
		// Inventory and planning must succeed for every selected root before an
		// initial binding or rebind becomes durable. Save before any data or
		// receipt write, including when this batch has no eligible files.
		if err := ctx.Err(); err != nil {
			return b, err
		}
		if err := d.save(); err != nil {
			return b, err
		}
	}
	c.Progress.plan(eligible)
	dst, err := local.NewFs(ctx, "destination", d.path, configmap.Simple{"encoding": encoder.EncodeZero.String()})
	if err != nil {
		return b, err
	}
	if err = b.transfer(ctx, src, dst, eligible); err != nil {
		return b, err
	}
	switch {
	case b.Result.Copied > 0:
		b.Result.Status = "copied"
	case b.Result.Deferred > 0:
		b.Result.Status = "deferred"
	default:
		b.Result.Status = "nothing-new"
	}
	return b, nil
}

func (b *Batch) planLog(o rfs.Object, action string) {
	if b.config.DryRun && b.config.PlanLogger != nil {
		b.config.PlanLogger.Debug("pull plan", "file", o.Remote(), "action", action, "bytes", o.Size())
	}
}

func inventoryDirectories(ctx context.Context, src rfs.Fs, only, dirs []string) ([]rfs.Object, []string, error) {
	budget := &listingBudget{}
	// Explicit roots count in the inventory even though List never returns them.
	for _, dir := range dirs {
		if dir != "" {
			budget.entries++
		}
	}
	ctx = smb.WithReadDir(ctx, budget.readDir)
	expressions, err := compilePatterns(only)
	if err != nil {
		return nil, nil, err
	}
	var objects []rfs.Object
	var directories []string
	count := 0
	for _, dir := range dirs {
		if dir != "" {
			count++
			if count > maxEntries {
				return nil, nil, failure(diagnostic.InventoryLimit)
			}
			directories = append(directories, dir)
		}
		err = walk.ListR(ctx, src, dir, true, -1, walk.ListAll, func(entries rfs.DirEntries) (err error) {
			if guarded, ok := src.(guardedSource); ok {
				defer guarded.boundary.recover(&err)
			}
			for _, e := range entries {
				count++
				if count > maxEntries {
					return failure(diagnostic.InventoryLimit)
				}
				relative := e.Remote()
				if dir != "" {
					var ok bool
					relative, ok = strings.CutPrefix(relative, dir+"/")
					if !ok {
						return failure(diagnostic.UnsafePath)
					}
				}
				folder, _, nested := strings.Cut(relative, "/")
				if !matches(expressions, folder) {
					continue
				}
				if err := validateRemote(e.Remote()); err != nil {
					return err
				}
				if _, ok := e.(rfs.Directory); ok && dir != "" {
					directories = append(directories, e.Remote())
				}
				if o, ok := e.(rfs.Object); ok {
					// Unfiltered MyWorks root files are included; only expressions select folders.
					if len(only) > 0 && !nested {
						continue
					}
					objects = append(objects, o)
				}
			}
			return nil
		})
		if err != nil {
			if dir != "" && errors.Is(err, rfs.ErrorDirNotFound) {
				return nil, nil, failure(diagnostic.MissingDirectory)
			}
			return nil, nil, err
		}
	}
	slices.SortFunc(objects, func(a, b rfs.Object) int { return strings.Compare(a.Remote(), b.Remote()) })
	return objects, directories, err
}

func folded(s string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < smallest {
				smallest = next
			}
		}
		return smallest
	}, norm.NFC.String(s))
}

func preflight(dest string, objects []rfs.Object, directories ...string) error {
	names := map[string]string{}
	addName := func(remote string) error {
		parts := strings.Split(remote, "/")
		for i := range parts {
			p := strings.Join(parts[:i+1], "/")
			key := folded(p)
			if old, ok := names[key]; ok && old != p {
				return failure(diagnostic.CaseCollision)
			}
			names[key] = p
		}
		return nil
	}
	for _, dir := range directories {
		if err := addName(dir); err != nil {
			return err
		}
		filename := filepath.Join(dest, filepath.FromSlash(dir))
		if err := checkLocalPath(filename); err != nil {
			return err
		}
		if info, err := os.Lstat(filename); err == nil && !info.IsDir() {
			return failure(diagnostic.UnsafePath)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, o := range objects {
		if err := addName(o.Remote()); err != nil {
			return err
		}
		filename := filepath.Join(dest, filepath.FromSlash(o.Remote()))
		rel, err := filepath.Rel(dest, filename)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return failure(diagnostic.UnsafePath)
		}
		if err = checkLocalPath(filename); err != nil {
			return err
		}
		if info, err := os.Lstat(filename); err == nil && !info.Mode().IsRegular() {
			return failure(diagnostic.UnsafePath)
		}
	}
	count := 0
	return filepath.WalkDir(dest, func(p string, e fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && p == dest {
			return nil
		}
		if err != nil {
			return err
		}
		if p == dest {
			return nil
		}
		rel, err := filepath.Rel(dest, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == metadata {
			return filepath.SkipDir
		}
		count++
		if count > maxEntries {
			return failure(diagnostic.InventoryLimit)
		}
		if remote, ok := names[folded(rel)]; ok && remote != rel {
			return failure(diagnostic.CaseCollision)
		}
		return nil
	})
}

type fileResult struct {
	name    string
	receipt Receipt
	copied  bool
	err     error
}

func (b *Batch) transfer(ctx context.Context, src, dst rfs.Fs, objects []rfs.Object) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan rfs.Object)
	results := make(chan fileResult, b.config.Transfers)
	var workers sync.WaitGroup
	for range b.config.Transfers {
		workers.Go(func() {
			defer func() {
				if recover() != nil {
					cancel()
					results <- fileResult{err: panicBoundary{logger: b.config.PlanLogger, abort: b.config.CancelOnPanic}.failure()}
				}
			}()
			for o := range jobs {
				if ctx.Err() != nil {
					return
				}
				b.config.Progress.start(o)
				r := transferFile(ctx, src, dst, b.destination.path, o, b.config.Verify || b.config.Recheck)
				results <- r
			}
		})
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(jobs)
		for _, o := range objects {
			select {
			case jobs <- o:
			case <-ctx.Done():
				return
			}
		}
	}()
	joined := make(chan struct{})
	go func() { workers.Wait(); close(results); close(joined) }()
	// Even a panic in the result/receipt owner must first cancel and drain the
	// workers. Their result sends cannot block while the operation unwinds.
	finished := false
	defer func() {
		cancel()
		if !finished && b.config.CancelOnPanic != nil {
			b.config.CancelOnPanic()
		}
		for range results {
		}
		<-done
		<-joined
	}()
	var failure error
	for r := range results {
		if r.err != nil {
			failure = errors.Join(failure, r.err)
			cancel()
			continue
		}
		// Attribute receipts only after the requested check and stable metadata.
		// Existing unreceipted bytes always require a remote comparison.
		r.receipt.Source = b.destination.State.Source
		r.receipt.Generation = b.destination.State.Generation
		b.destination.State.Files[r.name] = r.receipt
		b.Result.Verified++
		if r.copied {
			b.Result.Copied++
			b.Result.Bytes += r.receipt.Size
		}
		if err := b.destination.save(); err != nil {
			failure = errors.Join(failure, err)
			cancel()
		} else {
			b.config.Progress.complete(ctx, object.NewStaticObjectInfo(r.name, r.receipt.Mtime, r.receipt.Size, true, nil, src))
		}
	}
	finished = true
	return errors.Join(failure, ctx.Err())
}

func restat(ctx context.Context, src rfs.Fs, o rfs.ObjectInfo) (rfs.Object, error) {
	// Copy failures are restated to detect source changes. Once work is done,
	// reconnecting only feeds the backend's non-cancellable retry pacer.
	// Keep the original copy error; a canceled restat proves no metadata change.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := src.NewObject(ctx, o.Remote())
	if err != nil {
		return nil, err
	}
	if current.Size() != o.Size() || !current.ModTime(ctx).Equal(o.ModTime(ctx)) {
		return nil, failure(diagnostic.RemoteChanged)
	}
	return current, nil
}

func transferFile(ctx context.Context, src, dst rfs.Fs, dest string, o rfs.Object, reread bool) fileResult {
	r := fileResult{name: o.Remote()}
	r.receipt, r.copied, r.err = copyVerified(ctx, src, dst, dest, o, reread)
	return r
}

func copyVerified(ctx context.Context, src, dst rfs.Fs, dest string, listed rfs.Object, reread bool) (Receipt, bool, error) {
	// Keep expected metadata immutable: some backends refresh an object on Open.
	expected := object.NewStaticObjectInfo(listed.Remote(), listed.ModTime(ctx), listed.Size(), true, nil, src)
	o, err := restat(ctx, src, expected)
	if err != nil {
		return Receipt{}, false, err
	}
	existing, err := dst.NewObject(ctx, o.Remote())
	if err != nil && !errors.Is(err, rfs.ErrorObjectNotFound) {
		return Receipt{}, false, err
	}
	if err == nil && existing.Size() == o.Size() {
		same, err := operations.CheckIdenticalDownload(ctx, o, existing)
		if err != nil {
			return Receipt{}, false, err
		}
		if same {
			if _, err = restat(ctx, src, expected); err != nil {
				return Receipt{}, false, err
			}
			receipt, err := makeReceipt(ctx, expected, existing, checkReread)
			return receipt, false, err
		}
	}
	token, err := randomName()
	if err != nil {
		return Receipt{}, false, err
	}
	// The temp lives beside the final file: same filesystem and caller's data
	// umask/ACL. Its unguessable filename cannot enter receipts or the manifest.
	temporary := path.Join(path.Dir(o.Remote()), ".seestar-part-"+token)
	filename := filepath.Join(dest, filepath.FromSlash(temporary))
	defer os.Remove(filename)
	counted := &copySource{Object: o}
	copied, err := operations.Copy(ctx, dst, nil, temporary, counted)
	if err != nil {
		// A backend can detect corruption before our normal post-copy restat.
		// Preserve its error unless metadata independently proves a source change.
		if counted.eof.Load() && counted.bytes.Load() != expected.Size() {
			// An aborted copy's short EOF is not an independent verification failure.
			_, reason := diagnostic.Fields(err, "pull")
			if reason != "canceled" {
				err = &diagnostic.PullError{Reason: diagnostic.VerifyMismatch, Cause: err}
			}
		}
		_, changed := restat(ctx, src, expected)
		var detail *diagnostic.PullError
		if errors.As(changed, &detail) && detail.Reason == diagnostic.RemoteChanged {
			err = changed
		}
		return Receipt{}, false, err
	}
	if _, err = restat(ctx, src, expected); err != nil {
		return Receipt{}, false, err
	}
	// Check the actual temporary file, not a backend's cached Object.Size.
	info, err := os.Stat(filename)
	if err != nil {
		return Receipt{}, false, err
	}
	if info.Size() != expected.Size() {
		return Receipt{}, false, failure(diagnostic.VerifyMismatch)
	}
	if counted.bytes.Load() != expected.Size() {
		return Receipt{}, false, failure(diagnostic.VerifyMismatch)
	}
	check := checkSize
	if reread {
		same, err := operations.CheckIdenticalDownload(ctx, o, copied)
		if err != nil {
			return Receipt{}, false, err
		}
		if !same {
			return Receipt{}, false, failure(diagnostic.VerifyMismatch)
		}
		check = checkReread
	}
	if _, err = restat(ctx, src, expected); err != nil {
		return Receipt{}, false, err
	}
	receipt, err := makeReceipt(ctx, expected, copied, check)
	if err != nil {
		return Receipt{}, false, err
	}
	if err = ctx.Err(); err != nil {
		return Receipt{}, false, err
	}
	f, err := os.OpenFile(filename, os.O_RDWR, 0)
	if err != nil {
		return Receipt{}, false, err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return Receipt{}, false, err
	}
	final := filepath.Join(dest, filepath.FromSlash(o.Remote()))
	if err = checkLocalPath(final); err != nil {
		return Receipt{}, false, err
	}
	if err = os.Rename(filename, final); err != nil {
		return Receipt{}, false, err
	}
	if err = syncDirectory(filepath.Dir(final)); err != nil {
		return Receipt{}, false, err
	}
	return receipt, true, nil
}

func makeReceipt(ctx context.Context, remote rfs.ObjectInfo, local rfs.Object, check string) (Receipt, error) {
	sum, err := operations.HashSum(ctx, hash.SHA256, false, false, local)
	return Receipt{Size: remote.Size(), Mtime: remote.ModTime(ctx).UTC(), SHA256: sum, Check: check}, err
}

// Commit is called only after a fully verified batch and successful owned-session
// release. Explicit since/all never move auto checkpoints. A late interrupt also
// refuses commit. Manifest output contains only the recorded verified inventory.
func (b *Batch) Commit(ctx context.Context) error {
	if b.config.DryRun {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.Result.Status == "error" {
		return errors.New("cannot commit failed batch")
	}
	d := b.destination
	if b.config.Sums {
		if err := d.manifest(ctx); err != nil {
			return err
		}
	}
	if b.config.Since == "auto" {
		for key, selection := range b.selections {
			old := d.State.Selections[key]
			if old.Checkpoint.After(selection.Checkpoint) {
				selection.Checkpoint = old.Checkpoint
			}
			d.State.Selections[key] = selection
		}
	}
	return d.save()
}
