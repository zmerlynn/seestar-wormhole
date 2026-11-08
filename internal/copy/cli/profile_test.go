package cli

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// testProfile is the program's --profile default reader in these tests:
// nil, as for a program without one, unless a test sets it.
var testProfile LoadProfile

// useProfile makes --profile default read s for the rest of the test.
func useProfile(t *testing.T, s Saved) {
	t.Cleanup(func() { testProfile = nil })
	testProfile = func() (Saved, error) { return s, nil }
}

// fullSaved sets every member.
func fullSaved() Saved {
	return Saved{
		Dest: "/saved/dest", HistoryDir: "/saved/history", Endpoint: "192.0.2.7:4445",
		Excludes: []string{"saved-x1", "saved-x2"}, Includes: []string{"saved-i"}, NoDefaultExcludes: true,
		Interval: 9 * time.Minute, Once: true, Workers: 3,
	}
}

func none(string) bool { return false }

// given is a changed func for the flags names.
func given(names ...string) func(string) bool {
	return func(name string) bool { return slices.Contains(names, name) }
}

// Each command fills a flag not given from its saved member, and keeps a
// given one: the given value stays, and the flag isn't named as filled.
func TestProfileFillTable(t *testing.T) {
	s := fullSaved()
	sel := selectionFlags{excludes: s.Excludes, includes: s.Includes, noDefaultExcludes: true}

	var so syncOptions
	got := so.fillSaved(s, none)
	want := syncOptions{dest: s.Dest, historyDir: s.HistoryDir, endpoint: s.Endpoint, selection: sel, interval: s.Interval, once: true, workers: 3}
	if !reflect.DeepEqual(so, want) || !slices.Equal(got, []string{"--dest", "--history-dir", "--endpoint", "--exclude", "--include", "--no-default-excludes", "--interval", "--once", "--workers"}) {
		t.Errorf("sync filled %q: %+v", got, so)
	}
	explicit := syncOptions{dest: "/given", historyDir: "/given-h", endpoint: "h:1", selection: selectionFlags{excludes: []string{"given"}},
		interval: time.Minute, workers: 1}
	so = explicit
	if got := so.fillSaved(s, given("dest", "history-dir", "endpoint", "exclude", "include", "no-default-excludes", "interval", "once", "workers")); len(got) != 0 || !reflect.DeepEqual(so, explicit) {
		t.Errorf("sync with every flag given filled %q: %+v", got, so)
	}
	// A saved once gives way to a given --interval or --dry-run.
	for _, flag := range []string{"interval", "dry-run", "once"} {
		so = syncOptions{}
		if got := so.fillSaved(s, given(flag)); so.once || slices.Contains(got, "--once") {
			t.Errorf("sync with --%s took the saved once: %q", flag, got)
		}
	}

	var fo findOptions
	got = fo.fillSaved(s, none)
	if want := (findOptions{dest: s.Dest, historyDir: s.HistoryDir, endpoint: s.Endpoint, selection: sel}); !reflect.DeepEqual(fo, want) ||
		!slices.Equal(got, []string{"--dest", "--history-dir", "--endpoint", "--exclude", "--include", "--no-default-excludes"}) {
		t.Errorf("find filled %q: %+v", got, fo)
	}
	// One --exclude replaces the whole saved list, and leaves the rest.
	fo = findOptions{selection: selectionFlags{excludes: []string{"*.mp4"}}}
	got = fo.fillSaved(s, given("exclude"))
	if !slices.Equal(fo.selection.excludes, []string{"*.mp4"}) || !slices.Equal(fo.selection.includes, s.Includes) || slices.Contains(got, "--exclude") {
		t.Errorf("find --exclude filled %q: %+v", got, fo)
	}

	var bo browseOptions
	got = bo.fillSaved(s, none)
	if want := (browseOptions{dest: s.Dest, historyDir: s.HistoryDir, endpoint: s.Endpoint, selection: sel}); !reflect.DeepEqual(bo, want) || len(got) != 6 {
		t.Errorf("browse filled %q: %+v", got, bo)
	}
	// Without a destination, browse takes no selection.
	bo = browseOptions{}
	if got := bo.fillSaved(Saved{Endpoint: "h:1", Excludes: s.Excludes}, none); !slices.Equal(got, []string{"--endpoint"}) || bo.selection.excludes != nil {
		t.Errorf("browse without a destination filled %q: %+v", got, bo)
	}

	do := deleteOptions{olderThan: 7 * day}
	got = do.fillSaved(s, none)
	if want := (deleteOptions{dest: s.Dest, historyDir: s.HistoryDir, endpoint: s.Endpoint, olderThan: 7 * day}); !reflect.DeepEqual(do, want) ||
		!slices.Equal(got, []string{"--dest", "--history-dir", "--endpoint"}) {
		t.Errorf("delete filled %q: %+v", got, do)
	}
	do = deleteOptions{ignoreState: true}
	if got := do.fillSaved(s, none); !slices.Equal(got, []string{"--endpoint"}) || do.dest != "" || do.historyDir != "" {
		t.Errorf("delete --ignore-state filled %q: %+v", got, do)
	}
}

// A saved interval is not a given --interval: a dry run takes it.
func TestProfileDryRunWithSavedInterval(t *testing.T) {
	dev := newDevice()
	dev.set("MyWorks/a.fit", "abc", findT0)
	dev.use(t)
	t.Cleanup(readonly.Reset)
	s := Saved{Dest: filepath.Join(t.TempDir(), "dest"), HistoryDir: t.TempDir(), Endpoint: "h:445", Interval: 9 * time.Minute}
	useProfile(t, s)
	code, _, stderr := invoke(t, "sync", "--profile", "default", "--dry-run")
	if code != exitOK || !strings.Contains(stderr, `msg="saved settings" profile=default filled=--dest,--history-dir,--endpoint,--interval`+"\n") {
		t.Errorf("exit %d, want 0:\n%s", code, stderr)
	}
	if code, _, stderr := invoke(t, "sync", "--profile", "default", "--dry-run", "--interval", "1m"); code != exitUsage || !strings.Contains(stderr, "option=--dry-run") {
		t.Errorf("with a given --interval: exit %d, want 2:\n%s", code, stderr)
	}
}

// An imported destination is used: find and browse read its history and
// give each file its status, as with a given --dest.
func TestProfileDestinationIsUsed(t *testing.T) {
	statusDevice(t)
	dest, history := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "history")
	writeFindState(t, dest, history)
	useProfile(t, Saved{Dest: dest, HistoryDir: history, Endpoint: "h:445"})

	code, stdout, stderr := find(t, "--profile", "default", "--format", "jsonl")
	if want := `{"path":"MyWorks/a.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"copied"}`; code != exitOK || !strings.Contains(stdout, want) {
		t.Errorf("find: exit %d, lacks %s:\n%s\n%s", code, want, stdout, stderr)
	}

	b := startBrowseArgs(t, "browse", "--profile", "default")
	code, body := b.get(t, "api/list?path=MyWorks")
	if want := `"path":"MyWorks/a.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"copied"`; code != 200 || !strings.Contains(body, want) {
		t.Errorf("browse listing = %d, lacks %s:\n%s", code, want, body)
	}
	if code := b.end(); code != exitOK {
		t.Errorf("browse exit %d", code)
	}
}

// delete takes only where to act from a profile: the saved Copy patterns
// never select what it deletes, and its proof stays the SHA-256 one.
func TestProfileDeleteTakesNoSelection(t *testing.T) {
	s, recs := modesShare(t)
	s.use(t)
	dest, history := t.TempDir(), t.TempDir()
	place(t, dest, recs[0].path, recs[0].data, deleteOld)
	writeDeleteState(t, dest, history, "", recs...)
	useProfile(t, Saved{Dest: dest, HistoryDir: history, Endpoint: "h:445", Excludes: []string{"a.fit"}, Includes: []string{"*.jpg"},
		NoDefaultExcludes: true, Interval: time.Minute, Once: true, Workers: 1})
	t.Cleanup(readonly.Reset)
	code, _, stderr := invoke(t, "delete", "--profile", "default")
	if code != exitOK || !slices.Equal(logged(stderr, "would delete"), []string{"MyWorks/M 31/a.fit"}) ||
		!strings.Contains(stderr, `msg="deletion starting" proof=hash older_than=7d dry_run=true`) ||
		!strings.Contains(stderr, `msg="saved settings" profile=default filled=--dest,--history-dir,--endpoint`+"\n") {
		t.Errorf("exit %d:\n%s", code, stderr)
	}
}

// With --ignore-state, delete takes no local folder from a profile, so the
// rule against --dest with it doesn't fire.
func TestProfileDeleteIgnoreState(t *testing.T) {
	s, _ := modesShare(t)
	s.use(t)
	useProfile(t, Saved{Dest: t.TempDir(), HistoryDir: t.TempDir(), Endpoint: "h:445"})
	t.Cleanup(readonly.Reset)
	code, _, stderr := invoke(t, "delete", "--profile", "default", "--ignore-state", "--include", "*.fit")
	if code != exitOK || !strings.Contains(stderr, `msg="deletion starting" proof=none older_than=7d dry_run=true`) ||
		!strings.Contains(stderr, `msg="saved settings" profile=default filled=--endpoint`+"\n") {
		t.Errorf("exit %d:\n%s", code, stderr)
	}
}

// The record names the filled flags and none of their values.
func TestProfileRecord(t *testing.T) {
	statusDevice(t)
	dest, history := filepath.Join(t.TempDir(), "SENTINEL_DEST"), filepath.Join(t.TempDir(), "SENTINEL_HISTORY")
	writeFindState(t, dest, history)
	useProfile(t, Saved{Dest: dest, HistoryDir: history, Endpoint: "h:445", Excludes: []string{"SENTINEL_EXCLUDE"},
		Includes: []string{"SENTINEL_INCLUDE"}, NoDefaultExcludes: true, Interval: time.Minute, Once: true, Workers: 1})
	code, _, stderr := find(t, "--profile", "default")
	want := `level=INFO msg="saved settings" profile=default filled=--dest,--history-dir,--exclude,--include,--no-default-excludes` + "\n"
	if code != exitOK || strings.Count(stderr, `msg="saved settings"`) != 1 || !strings.Contains(stderr, want) || strings.Contains(stderr, "SENTINEL") {
		t.Errorf("exit %d, want one record %q and no value:\n%s", code, want, stderr)
	}
}

// Another name is refused before anything is read; a reader's refusal is
// the command's; a program without a reader refuses --profile.
func TestProfileRefusals(t *testing.T) {
	t.Cleanup(func() { testProfile = nil })
	testProfile = func() (Saved, error) { t.Error("read a profile for another name"); return Saved{}, nil }
	for _, cmd := range []string{"sync", "find", "browse", "delete"} {
		for _, name := range []string{"scope2", "", "Default"} {
			code, stdout, stderr := invoke(t, cmd, "--profile", name)
			if code != exitUsage || stdout != "" || !strings.Contains(stderr, `option=--profile rule="only default exists in this version"`) {
				t.Errorf("%s --profile %q: exit %d:\n%s", cmd, name, code, stderr)
			}
		}
	}
	testProfile = func() (Saved, error) {
		return Saved{}, &diagnostic.OptionError{Option: "--profile", Rule: "synthetic refusal"}
	}
	if code, _, stderr := invoke(t, "find", "--profile", "default"); code != exitUsage || !strings.Contains(stderr, `option=--profile rule="synthetic refusal"`) {
		t.Errorf("a refused profile: exit %d:\n%s", code, stderr)
	}
	testProfile = nil
	if code, _, stderr := invoke(t, "find", "--profile", "default"); code != exitUsage || !strings.Contains(stderr, "option=--profile") {
		t.Errorf("no reader: exit %d:\n%s", code, stderr)
	}
}

// Without --profile nothing is read, and a saved endpoint fills nothing.
func TestProfileNotRead(t *testing.T) {
	t.Cleanup(func() { testProfile = nil })
	testProfile = func() (Saved, error) { t.Error("read a profile without --profile"); return fullSaved(), nil }
	for _, cmd := range []string{"sync", "find", "browse", "delete"} {
		if code, _, stderr := invoke(t, cmd); code != exitUsage || !strings.Contains(stderr, "option=--endpoint") {
			t.Errorf("%s: exit %d:\n%s", cmd, code, stderr)
		}
	}
	// A profile without an endpoint leaves --endpoint required.
	useProfile(t, Saved{Dest: t.TempDir()})
	if code, _, stderr := invoke(t, "sync", "--profile", "default"); code != exitUsage || !strings.Contains(stderr, `option=--endpoint rule="required; give the SMB host:port"`) {
		t.Errorf("no saved endpoint: exit %d:\n%s", code, stderr)
	}
}
