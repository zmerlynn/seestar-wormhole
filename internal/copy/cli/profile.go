package cli

import (
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// DefaultProfile is the only profile: the app's own saved settings.
const DefaultProfile = "default"

// Saved is what a profile holds for the file commands. A zero member is
// one it doesn't set, and fills no flag.
type Saved struct {
	// Dest is the folder of the telescope saved in the app. NoFolder is
	// true when that telescope has none yet while another has one: a
	// command that would fill --dest then fails until one is chosen.
	Dest       string
	NoFolder   bool
	HistoryDir string
	Endpoint   string
	// LANSerial is the serial the app saved with Endpoint on this
	// network, or "". A run that dials the filled Endpoint follows the
	// telescope by it when it moved.
	LANSerial string
	// LANScan is when the app scans for the telescope saved with
	// LANSerial: start, connect or off, or "" (start). --scan overrides
	// it.
	LANScan  string
	Excludes []string
	Includes []string
	// NoDefaultExcludes is true when the saved settings don't skip what
	// the built-in patterns match.
	NoDefaultExcludes bool
	Interval          time.Duration
	Once              bool
	Workers           int
	// Fallbacks are the saved values the load replaced, logged when the
	// command runs.
	Fallbacks []diagnostic.Fallback
}

// LoadProfile reads the default profile, without writing any file. A
// failure is a *diagnostic.OptionError for --profile. The program supplies
// it, so this package never reads the app's files itself.
type LoadProfile func() (Saved, error)

// profileFlag is --profile, and the flags it filled.
type profileFlag struct {
	name      string
	set       bool
	filled    []string
	fallbacks []diagnostic.Fallback
	// statuses is set by a command that runs without a local folder,
	// only without the statuses one gives, as browse does: a saved
	// telescope without a folder fills no --dest there, and is no error.
	statuses bool
}

func (p *profileFlag) register(f *pflag.FlagSet) {
	f.StringVar(&p.name, "profile", "", "fill the flags not given from the app's saved settings; only default exists (default: read no saved settings)")
}

// load reads the profile when --profile was given. It checks the name
// before anything is read.
func (p *profileFlag) load(flags *pflag.FlagSet, load LoadProfile) (Saved, bool, error) {
	if p.set = flags.Changed("profile"); !p.set {
		return Saved{}, false, nil
	}
	if p.name != DefaultProfile {
		return Saved{}, false, &diagnostic.OptionError{Option: "--profile", Rule: "only default exists in this version"}
	}
	if load == nil {
		return Saved{}, false, &diagnostic.OptionError{Option: "--profile", Rule: "no per-user config folder for the default profile; give the flags"}
	}
	s, err := load()
	return s, err == nil, err
}

// fill loads the profile when --profile was given and fills the flags
// not given with fillSaved.
func (p *profileFlag) fill(flags *pflag.FlagSet, load LoadProfile, fillSaved func(Saved, func(string) bool) []string) error {
	s, ok, err := p.load(flags, load)
	if !ok {
		return err
	}
	if s.NoFolder && !flags.Changed("dest") && !p.statuses && takesDest(flags) {
		return &diagnostic.OptionError{Option: "--profile", Rule: errNoFolder}
	}
	p.filled = fillSaved(s, flags.Changed)
	p.fallbacks = s.Fallbacks
	return nil
}

// errNoFolder is --profile's rule when the telescope saved in the app has
// no folder yet. It names neither the telescope nor a folder.
const errNoFolder = "the telescope saved in the app has no folder yet; choose one on the app's Copy tab, or give --dest"

// takesDest reports whether the command uses a local folder: all do but
// delete with --ignore-state.
func takesDest(flags *pflag.FlagSet) bool {
	ignore, err := flags.GetBool("ignore-state")
	return err != nil || !ignore
}

// present reports whether the flag name was given or filled: what a rule
// that needs a value, and the run, go by.
func (p *profileFlag) present(flags *pflag.FlagSet, name string) bool {
	return flags.Changed(name) || slices.Contains(p.filled, "--"+name)
}

// log records which flags the profile filled, never their values, and
// the saved values its load replaced.
func (p *profileFlag) log(log *slog.Logger) {
	if p.set {
		diagnostic.LogFallbacks(log, p.fallbacks)
		log.Info("saved settings", "profile", p.name, "filled", strings.Join(p.filled, ","))
	}
}

// filler fills the flags the command line didn't give and keeps their
// names. A filled flag is never marked given: what a rule forbids is
// judged by changed alone.
type filler struct {
	changed func(string) bool
	filled  *[]string
}

func (f filler) take(name string, ok bool) bool {
	if !ok || f.changed(name) {
		return false
	}
	*f.filled = append(*f.filled, "--"+name)
	return true
}

func (f filler) str(name string, dst *string, v string) {
	if f.take(name, v != "") {
		*dst = v
	}
}

// list replaces nothing given once on the command line: a repeatable flag
// given once replaces the whole saved list.
func (f filler) list(name string, dst *[]string, v []string) {
	if f.take(name, len(v) > 0) {
		*dst = append([]string{}, v...)
	}
}

func (f filler) on(name string, dst *bool, v bool) {
	if f.take(name, v) {
		*dst = true
	}
}

// fill fills the selection flags from s.
func (sel *selectionFlags) fill(f filler, s Saved) {
	f.list("exclude", &sel.excludes, s.Excludes)
	f.list("include", &sel.includes, s.Includes)
	f.on("no-default-excludes", &sel.noDefaultExcludes, s.NoDefaultExcludes)
}

// fillSaved fills sync's flags from s and returns the names filled.
func (o *syncOptions) fillSaved(s Saved, changed func(string) bool) []string {
	var filled []string
	f := filler{changed, &filled}
	f.str("dest", &o.dest, s.Dest)
	f.str("history-dir", &o.historyDir, s.HistoryDir)
	f.str("endpoint", &o.endpoint, s.Endpoint)
	o.follow = newFollow(s, changed)
	o.selection.fill(f, s)
	if f.take("interval", s.Interval > 0) {
		o.interval = s.Interval
	}
	// A saved once gives way to any flag that says how often to pass.
	f.on("once", &o.once, s.Once && !changed("interval") && !changed("dry-run"))
	if f.take("workers", s.Workers > 0) {
		o.workers = s.Workers
	}
	return filled
}

// fillSaved fills find's flags from s and returns the names filled.
func (o *findOptions) fillSaved(s Saved, changed func(string) bool) []string {
	var filled []string
	f := filler{changed, &filled}
	f.str("dest", &o.dest, s.Dest)
	f.str("history-dir", &o.historyDir, s.HistoryDir)
	f.str("endpoint", &o.endpoint, s.Endpoint)
	o.follow = newFollow(s, changed)
	o.selection.fill(f, s)
	return filled
}

// fillSaved fills browse's flags from s and returns the names filled. The
// selection is filled only with a destination, as browse uses it only
// for statuses. A saved telescope without a folder, when --dest isn't
// given, fills only where to browse: the page lists without statuses.
func (o *browseOptions) fillSaved(s Saved, changed func(string) bool) []string {
	var filled []string
	f := filler{changed, &filled}
	if !s.NoFolder || changed("dest") {
		f.str("dest", &o.dest, s.Dest)
		f.str("history-dir", &o.historyDir, s.HistoryDir)
	}
	f.str("endpoint", &o.endpoint, s.Endpoint)
	o.follow = newFollow(s, changed)
	if o.dest != "" {
		o.selection.fill(f, s)
	}
	return filled
}

// fillSaved fills delete's flags from s and returns the names filled:
// only where to act. Its selection and proof stay as given, and with
// --ignore-state it takes no local folder at all.
func (o *deleteOptions) fillSaved(s Saved, changed func(string) bool) []string {
	var filled []string
	f := filler{changed, &filled}
	if !o.ignoreState {
		f.str("dest", &o.dest, s.Dest)
		f.str("history-dir", &o.historyDir, s.HistoryDir)
	}
	f.str("endpoint", &o.endpoint, s.Endpoint)
	o.follow = newFollow(s, changed)
	if o.follow != nil {
		// A run deletes where it first connected: a later connect,
		// the remover's among them, never scans again.
		o.follow.pin = true
	}
	return filled
}
