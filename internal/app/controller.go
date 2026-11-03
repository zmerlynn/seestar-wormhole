package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/copier"
	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// The copy loop's options that Settings doesn't hold are the CLI's
// defaults.
const (
	readyAge     = 2 * time.Minute
	firstPassAge = 10 * time.Minute
	workers      = 4
)

// The Copy tab's states, as the page names them.
const (
	stOff         = "off"
	stConnecting  = "connecting"
	stListing     = "listing"
	stCopying     = "copying"
	stUpToDate    = "uptodate"    // waiting after a pass with no failures
	stFailures    = "failures"    // waiting after a pass with failed files
	stListFail    = "listfail"    // waiting after a listing failed
	stUnreachable = "unreachable" // waiting after the connection failed
	stStopping    = "stopping"
	stStopped     = "stopped" // stopped during a pass's downloads
	stInUse       = "inuse"
	stCantWrite   = "cantwrite" // the loop ended with an error, other than the lock
)

// checkReach is what to check when the telescope can't be reached.
const checkReach = "Check that the telescope is on and on the same network as this computer, or that seestar-wormhole is running."

// errBusy is a start while copying is on or stopping, or the app quitting.
var errBusy = errors.New("copying is already on")

// controller runs the copy loop, at most one at a time, and keeps its
// status. Its mutex is never held while waiting on the engine, the disk,
// the network or the log.
type controller struct {
	parent   context.Context
	stateDir string
	log      *slog.Logger // the loop's: it also feeds activity
	activity *activity
	// dial opens a pass's session; tests replace it.
	dial     func(ctx context.Context, endpoint string) (remote.Session, error)
	checkNow chan struct{}

	mu       sync.Mutex
	cancel   context.CancelFunc // nil when no loop runs
	done     chan struct{}      // closed when the loop has returned
	stopping bool
	quitting bool
	st       status
}

// status is the loop's state, for the page.
type status struct {
	state    string
	endpoint string
	folder   string
	interval time.Duration
	contact  string // the last contact: "", "connecting", "ok" or "err"

	// The current pass.
	unreachable   bool // its connection failed
	folders       int
	listed        string // the folder being listed
	files         int    // planned downloads
	bytes         int64
	finished      int   // downloads finished, copied or failed
	finishedBytes int64 // bytes of those copied
	adopted       int
	flight        map[string][2]int64 // done and size of each download running
	copyStart     time.Time
	meter         meter

	last    *engine.Summary // the last pass that listed the share
	passErr error           // why the last pass failed before copying
	next    time.Time       // the next pass's start, while waiting

	// A stop during downloads.
	stopCopied, stopDiscarded int
	wasCopying                bool

	startErr error // why the loop couldn't start, or go on
}

func newController(parent context.Context, stateDir string, log *slog.Logger, dial func(context.Context, string) (remote.Session, error)) *controller {
	act := &activity{}
	return &controller{
		parent: parent, stateDir: stateDir, activity: act,
		log:      slog.New(newTee(log.Handler(), act)),
		dial:     dial,
		checkNow: make(chan struct{}),
		st:       status{state: stOff},
	}
}

// start turns copying on with set, which the loop keeps for its life.
func (c *controller) start(set Settings) error {
	sel, err := selector(set)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.done != nil || c.quitting {
		c.mu.Unlock()
		return errBusy
	}
	ctx, cancel := context.WithCancel(c.parent)
	done := make(chan struct{})
	c.cancel, c.done, c.stopping = cancel, done, false
	interval := time.Duration(set.IntervalMinutes) * time.Minute
	c.st = status{state: stConnecting, endpoint: set.Address, folder: set.Folder, interval: interval, contact: c.st.contact, last: c.st.last}
	o := engine.SyncOptions{
		Endpoint: set.Address, Dest: set.Folder, StateDir: c.stateDir, Selector: sel, Interval: interval,
		ReadyAge: readyAge, FirstPassAge: firstPassAge, Workers: workers,
		Dial: c.dialPass, Hooks: c.hooks(ctx), CheckNow: c.checkNow, Recreate: true,
	}
	c.mu.Unlock()
	c.log.Info("copying started", "endpoint", set.Address, "dest", set.Folder)
	go func() {
		ticks := time.NewTicker(time.Second)
		ran := make(chan error, 1)
		go func() { ran <- engine.RunSync(ctx, o, c.log) }()
		var err error
	wait:
		for {
			select {
			case <-ticks.C:
				c.sample(time.Now())
			case err = <-ran:
				break wait
			}
		}
		ticks.Stop()
		cancel()
		c.finish(err)
		c.mu.Lock()
		c.cancel, c.done = nil, nil
		close(done)
		c.mu.Unlock()
	}()
	return nil
}

// selector is the CLI's selection for set's patterns.
func selector(set Settings) (engine.Selector, error) {
	excludes := set.Excludes
	if set.SkipBuiltIn {
		excludes = append(append([]string{}, engine.DefaultExcludes...), excludes...)
	}
	var sel engine.Selector
	for _, list := range []struct {
		in  []string
		out *[]engine.Pattern
	}{{excludes, &sel.Excludes}, {set.Includes, &sel.Includes}} {
		for _, s := range list.in {
			p, err := engine.ParsePattern(s)
			if err != nil {
				return engine.Selector{}, fmt.Errorf("pattern %q: %w", s, err)
			}
			*list.out = append(*list.out, p)
		}
	}
	return sel, nil
}

// finish records how the loop ended.
func (c *controller) finish(err error) {
	c.mu.Lock()
	st := &c.st
	switch {
	case errors.Is(err, state.ErrLocked):
		st.state = stInUse
	case err != nil:
		st.state, st.startErr = stCantWrite, err
	case c.stopping && st.wasCopying:
		st.state = stStopped
	default:
		st.state = stOff
	}
	c.mu.Unlock()
	if err != nil {
		c.log.Error("copying stopped", "err", err)
	} else {
		c.log.Info("copying stopped")
	}
}

// stop turns copying off: the loop's context ends, which discards the
// files in flight. The state stays "stopping" until the loop returns.
func (c *controller) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel == nil || c.stopping {
		return
	}
	c.stopping = true
	st := &c.st
	st.wasCopying = st.state == stCopying
	st.state = stStopping
	c.cancel()
}

// check starts the next pass now, if the loop is waiting for it.
func (c *controller) check() {
	select {
	case c.checkNow <- struct{}{}:
	default:
	}
}

// quit stops copying for good and waits for the loop to return.
func (c *controller) quit() {
	c.mu.Lock()
	c.quitting = true
	if c.cancel != nil {
		c.cancel()
	}
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

// on reports whether copying is on, or still stopping.
func (c *controller) on() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done != nil
}

// dialPass opens a pass's session, which starts the pass.
func (c *controller) dialPass(ctx context.Context, endpoint string) (remote.Session, error) {
	c.mu.Lock()
	st := &c.st
	if ctx.Err() == nil {
		last, contact := st.last, st.contact
		*st = status{state: stConnecting, endpoint: st.endpoint, folder: st.folder, interval: st.interval, last: last, contact: contact}
		st.contact = "connecting"
	}
	c.mu.Unlock()
	sess, err := c.dial(ctx, endpoint)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return sess, err
	}
	if err != nil {
		st.unreachable, st.contact = true, "err"
	} else {
		st.state, st.contact = stListing, "ok"
	}
	return sess, err
}

// hooks turns the engine's calls into the status. Calls after ctx is
// done, from a stopped pass, change only the stop's counts.
func (c *controller) hooks(ctx context.Context) engine.Hooks {
	return engine.Hooks{
		Listed: func(p string, n int) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if ctx.Err() == nil {
				c.st.state, c.st.folders, c.st.listed = stListing, n, p
			}
		},
		Planned: func(files int, bytes int64) {
			c.mu.Lock()
			defer c.mu.Unlock()
			st := &c.st
			st.files, st.bytes, st.listed = files, bytes, ""
			if files > 0 && ctx.Err() == nil {
				now := time.Now()
				st.state, st.copyStart = stCopying, now
				st.flight = map[string][2]int64{}
				st.meter = meter{}
				st.meter.add(now, 0)
			}
		},
		Copying: func(p string, done, size int64) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.st.flight != nil {
				c.st.flight[p] = [2]int64{done, size}
			}
		},
		Done: func(r copier.Result) {
			c.mu.Lock()
			defer c.mu.Unlock()
			st := &c.st
			_, started := st.flight[r.Decision.Path]
			delete(st.flight, r.Decision.Path)
			if started && r.Err != nil && ctx.Err() != nil {
				st.stopDiscarded++
			}
			switch {
			case r.Decision.Kind != plan.Copy && r.Decision.Kind != plan.Replace:
				if r.Err == nil {
					st.adopted++
				}
			case r.Err == nil:
				st.finished++
				st.finishedBytes += r.Record.Size
			default:
				st.finished++
			}
		},
		PassDone: func(sum engine.Summary) {
			c.mu.Lock()
			defer c.mu.Unlock()
			st := &c.st
			st.flight = nil
			if ctx.Err() != nil {
				st.stopCopied = sum.Copied + sum.Replaced
				return
			}
			st.next = time.Now().Add(st.interval)
			st.passErr = sum.Err
			switch {
			case sum.Err != nil && st.unreachable:
				st.state = stUnreachable
			case sum.Err != nil:
				st.state = stListFail
			case len(sum.Failed) > 0:
				st.last, st.state = &sum, stFailures
			default:
				st.last, st.state = &sum, stUpToDate
			}
		},
	}
}

// sample adds the bytes copied so far to the speed meter, while copying.
func (c *controller) sample(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.state == stCopying {
		c.st.meter.add(now, c.st.copied())
	}
}

// copied is the bytes the pass has copied so far, in flight or done.
func (st *status) copied() int64 {
	n := st.finishedBytes
	for _, f := range st.flight {
		n += f[0]
	}
	return n
}

// speedWindow is how far back the speed is averaged; time left shows only
// after copying this long. minSpeed is the speed below which time left is
// hidden.
const (
	speedWindow = 10 * time.Second
	minSpeed    = 100e3 // bytes a second
)

// meter keeps the samples of the last speedWindow, and the one before.
type meter struct {
	samples []sample
}

type sample struct {
	t     time.Time
	bytes int64
}

func (m *meter) add(t time.Time, bytes int64) {
	m.samples = append(m.samples, sample{t, bytes})
	for len(m.samples) > 2 && !m.samples[1].t.After(t.Add(-speedWindow)) {
		m.samples = m.samples[1:]
	}
}

// speed is the bytes a second over the samples kept; ok is false before
// there are two.
func (m *meter) speed() (float64, bool) {
	if len(m.samples) < 2 {
		return 0, false
	}
	first, last := m.samples[0], m.samples[len(m.samples)-1]
	dt := last.t.Sub(first.t).Seconds()
	if dt <= 0 {
		return 0, false
	}
	return float64(last.bytes-first.bytes) / dt, true
}

// timeLeft words the time to copy remaining bytes at speed, once copying
// has run for speedWindow and only while speed is at least minSpeed.
func timeLeft(copying time.Duration, speed float64, remaining int64) string {
	if copying < speedWindow || speed < minSpeed {
		return ""
	}
	secs := float64(remaining) / speed
	if secs < 60 {
		return "Less than a minute left"
	}
	m := int(math.Round(secs / 60))
	switch {
	case m < 60:
		return fmt.Sprintf("About %d min left", m)
	case m%60 == 0:
		return fmt.Sprintf("About %d h left", m/60)
	default:
		return fmt.Sprintf("About %d h %d min left", m/60, m%60)
	}
}

// copyView is the Copy tab's state and its text, for the page.
type copyView struct {
	State    string        `json:"state"`
	On       bool          `json:"on"` // copying is on or stopping: Settings are locked
	Chip     string        `json:"chip"`
	Contact  string        `json:"contact"`
	Title    string        `json:"title"`
	Text     string        `json:"text,omitempty"`
	Path     string        `json:"path,omitempty"` // what is being looked at
	Summary  string        `json:"summary,omitempty"`
	Settling string        `json:"settling,omitempty"`
	Notice   string        `json:"notice,omitempty"`
	Failed   []failure     `json:"failed,omitempty"`
	Progress *progressView `json:"progress,omitempty"`
	Preview  *previewView  `json:"preview,omitempty"`
}

type failure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type progressView struct {
	Done   int64    `json:"done"` // the bar's value and max
	Total  int64    `json:"total"`
	Bytes  string   `json:"bytes"`
	Speed  string   `json:"speed,omitempty"`
	Left   string   `json:"left,omitempty"`
	Found  string   `json:"found,omitempty"`
	Flight []flight `json:"flight"`
}

type flight struct {
	Path    string `json:"path"`
	Percent int    `json:"percent"`
}

// view is the status as the page shows it at now.
func (c *controller) view(now time.Time) copyView {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := &c.st
	v := copyView{State: st.state, On: c.done != nil, Contact: st.contact}
	at := func(t time.Time) string { return t.Local().Format("15:04") }
	switch st.state {
	case stOff:
		v.Title = "Copying is off."
	case stConnecting:
		v.Title = "Connecting to " + st.endpoint + "…"
	case stListing:
		v.Title = "Looking for new files…"
		if st.folders > 0 {
			v.Title += " " + count(st.folders, "folder", "folders") + " checked."
		}
		v.Path = listingLine(st.listed)
	case stCopying:
		v.Title = fmt.Sprintf("Copying %d of %s", min(st.finished+len(st.flight), st.files), count(st.files, "file", "files"))
		v.Progress = st.progress(now)
	case stUpToDate:
		v.Title = "Up to date. Next check at " + at(st.next) + "."
	case stFailures:
		n := len(st.last.Failed)
		v.Title = "Check finished with " + count(n, "file", "files") + " not copied. Next check at " + at(st.next) + "."
		v.Notice = count(n, "file", "files") + " couldn't be copied. They'll be tried again at the next check."
		if n == 1 {
			v.Notice = "1 file couldn't be copied. It will be tried again at the next check."
		}
		for _, f := range st.last.Failed {
			v.Failed = append(v.Failed, failure{f.Path, f.Reason})
		}
	case stListFail:
		v.Title = "Couldn't read " + unread(st.passErr) + "."
		v.Text = "No files were copied. Trying again at " + at(st.next) + "."
	case stUnreachable:
		v.Title = "Can't reach the telescope at " + st.endpoint + "."
		v.Text = "Trying again at " + at(st.next) + ". " + checkReach + " Copying stays on and keeps trying."
	case stStopping:
		v.Title = "Stopping…"
	case stStopped:
		v.Title = "Stopped."
		var parts []string
		if st.stopCopied > 0 {
			parts = append(parts, count(st.stopCopied, "file was", "files were")+" copied.")
		}
		if n := st.stopDiscarded; n == 1 {
			parts = append(parts, "The 1 in progress was discarded and will be copied next time.")
		} else if n > 1 {
			parts = append(parts, fmt.Sprintf("The %d in progress were discarded and will be copied next time.", n))
		}
		v.Text = strings.Join(parts, " ")
	case stInUse:
		v.Title = "Another copy is already using this folder."
		v.Text = "It's in another seestar-copy window or seestar-copy-cli. Stop that copy, then start copying here."
	case stCantWrite:
		// Only a refused write in the folder names it as the cause.
		if errors.Is(st.startErr, fs.ErrPermission) && inFolder(st.startErr, st.folder) {
			v.Title = "Can't write to " + st.folder + ": " + reason(st.startErr) + "."
			v.Text = "Choose a writable folder in Settings, then start copying."
		} else {
			v.Title = "Couldn't start copying: " + st.startErr.Error() + "."
			v.Text = "Fix the problem, then start copying."
		}
	}
	switch st.state {
	case stOff, stUpToDate, stFailures:
		if st.last != nil {
			v.Summary = summaryLine(*st.last)
		}
	}
	if (st.state == stUpToDate || st.state == stFailures) && st.last.Waiting > 0 {
		v.Settling = count(st.last.Waiting, "new file is", "new files are") + " still being written. They'll be copied on a later check."
	}
	switch {
	case st.state == stConnecting || st.state == stListing || st.state == stCopying:
		v.Chip = "Copying"
	case st.state == stUpToDate:
		v.Chip = "Up to date"
	case st.state == stFailures:
		v.Chip = count(len(st.last.Failed), "file", "files") + " not copied"
	case v.On:
		v.Chip = "Copying is on"
	default:
		v.Chip = "Copying is off"
	}
	return v
}

func (st *status) progress(now time.Time) *progressView {
	done := st.copied()
	p := &progressView{Done: done, Total: max(st.bytes, 1), Bytes: size(float64(done)) + " of " + size(float64(st.bytes)), Flight: []flight{}}
	// A failed download's bytes leave the count, so the speed can drop
	// below zero for a while; it is hidden then.
	if speed, ok := st.meter.speed(); ok && speed > 0 {
		p.Speed = size(speed) + "/s"
		p.Left = timeLeft(now.Sub(st.copyStart), speed, st.bytes-done)
	}
	if st.adopted > 0 {
		p.Found = fmt.Sprintf("Found already in your folder: %d", st.adopted)
	}
	for path, f := range st.flight {
		pct := 100
		if f[1] > 0 {
			pct = int(f[0] * 100 / f[1])
		}
		p.Flight = append(p.Flight, flight{path, pct})
	}
	slices.SortFunc(p.Flight, func(a, b flight) int { return strings.Compare(a.Path, b.Path) })
	return p
}

// inFolder reports whether err's path is folder or inside it.
func inFolder(err error, folder string) bool {
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		return false
	}
	rel, err := filepath.Rel(folder, pe.Path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// unread names the folder a failed listing couldn't read.
func unread(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Path != "" && pe.Path != "." {
		return strings.ReplaceAll(pe.Path, `\`, "/")
	}
	return "the telescope's files"
}

// summaryLine words a pass's counts.
func summaryLine(s engine.Summary) string {
	var parts []string
	if n := s.Copied + s.Replaced; n == 0 {
		parts = append(parts, "No files were copied.")
	} else {
		parts = append(parts, fmt.Sprintf("Copied %s (%s) in %s.", count(n, "file", "files"), size(float64(s.Bytes)), elapsed(s.Elapsed)))
	}
	if n := s.Adopted; n > 0 {
		parts = append(parts, count(n, "was", "were")+" already in your folder.")
	}
	if n := s.Replaced; n > 0 {
		parts = append(parts, count(n, "changed on the telescope and was", "changed on the telescope and were")+" copied again.")
	}
	if n := s.Waiting; n > 0 {
		parts = append(parts, count(n, "is", "are")+" still being written.")
	}
	if n := len(s.Failed); n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed.", n))
	}
	return strings.Join(parts, " ")
}

// listingLine words the folder being listed, if any, for the line under a
// stage's title. The title alone is announced.
func listingLine(folder string) string {
	if folder == "" {
		return ""
	}
	return "Listing " + folder + "…"
}

// count is n with the singular or plural words after it.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// size words a byte count in decimal units, with one decimal below 100.
func size(n float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for n >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	switch {
	case i == 0:
		return fmt.Sprintf("%d B", int64(n))
	case n < 100:
		return fmt.Sprintf("%.1f %s", n, units[i])
	default:
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
}

// elapsed words a pass's duration: "2 min 31 s".
func elapsed(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	switch {
	case s < 1:
		return "less than a second"
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min %d s", s/60, s%60)
	default:
		return fmt.Sprintf("%d h %d min", s/3600, s%3600/60)
	}
}
