package state

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendWritesLiteralLine(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	zone := time.FixedZone("UTC+2", 2*60*60)
	rec := Record{
		Path:   "MyWorks/M 31 & <M 32>/Light_1.fit",
		Size:   16594560,
		MTime:  time.Date(2026, 10, 29, 5, 31, 19, 1, zone),
		SHA256: shaA,
		At:     time.Date(2026, 11, 2, 3, 2, 3, 120000000, zone),
		How:    Copied,
	}
	if err := s.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	p, _ := Path(stateDir, dest)
	want := headerLine(dest) +
		`{"path":"MyWorks/M 31 & <M 32>/Light_1.fit","size":16594560,"mtime":"2026-10-29T03:31:19.000000001Z","sha256":"` + shaA + `","at":"2026-11-02T01:02:03.12Z","how":"copied"}` + "\n"
	if got := readFile(t, p); got != want {
		t.Errorf("state file =\n%s\nwant\n%s", got, want)
	}

	// The nanoseconds survive a reload exactly.
	s = mustOpen(t, stateDir, dest, nil)
	got, ok := s.Lookup(rec.Path)
	if !ok || !got.MTime.Equal(rec.MTime) || got.MTime.UnixNano() != rec.MTime.UnixNano() || !got.At.Equal(rec.At) {
		t.Errorf("reloaded record = %+v, %v, want mtime %v and at %v", got, ok, rec.MTime, rec.At)
	}
}

func TestAppendLastWins(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	history := []Record{
		m31Copied(t),
		{Path: "MyWorks/M 31/Light_1.fit", Size: 7, MTime: mustTime(t, "2026-10-30T00:00:00Z"), SHA256: shaB, At: mustTime(t, "2026-11-03T00:00:00Z"), How: Replaced},
		{Path: "MyWorks/M 31/Light_1.fit", Size: 8, MTime: mustTime(t, "2026-10-31T00:00:00Z"), SHA256: shaC, At: mustTime(t, "2026-11-04T00:00:00Z"), How: Adopted},
	}
	for i, rec := range history {
		if err := s.Append(rec); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if got, _ := s.Lookup(rec.Path); got.How != rec.How || got.Size != rec.Size {
			t.Errorf("after Append %d, Lookup = %+v, want %+v", i, got, rec)
		}
	}
	s.Close()
	s = mustOpen(t, stateDir, dest, nil)
	checkRecords(t, s.Snapshot(), map[string]Record{history[2].Path: history[2]})
}

func TestAppendAfterTornTail(t *testing.T) {
	stateDir, dest := dirs(t)
	good := headerLine(dest) + lineM31Copied
	p := writeState(t, stateDir, dest, good+`{"path":"Autorun/Dark/d.fit","size":0,"mti`)
	s := mustOpen(t, stateDir, dest, nil)
	rec := Record{Path: "Autorun/Dark/d.fit", Size: 0, MTime: mustTime(t, "2026-10-29T01:00:00Z"), SHA256: shaA, At: mustTime(t, "2026-11-02T01:02:03Z"), How: Copied}
	if err := s.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	s.Close()
	if got, want := readFile(t, p), good+lineDark; got != want {
		t.Errorf("state file =\n%s\nwant\n%s", got, want)
	}
	log, logs := logBuffer()
	s = mustOpen(t, stateDir, dest, log)
	checkRecords(t, s.Snapshot(), map[string]Record{m31Copied(t).Path: m31Copied(t), rec.Path: rec})
	if logs.Len() != 0 {
		t.Errorf("reload logged %q, want nothing", logs.String())
	}
}

func TestAppendRejects(t *testing.T) {
	valid := Record{Path: "a/b.fit", Size: 1, MTime: time.Unix(1, 0), SHA256: shaA, At: time.Unix(2, 0), How: Copied}
	for _, tc := range []struct {
		name string
		edit func(*Record)
		want string
	}{
		{"empty path", func(r *Record) { r.Path = "" }, "path is empty"},
		{"dot", func(r *Record) { r.Path = "." }, "not relative"},
		{"dot dot", func(r *Record) { r.Path = ".." }, "not relative"},
		{"dot dot inside", func(r *Record) { r.Path = "a/../b" }, "not relative"},
		{"dot inside", func(r *Record) { r.Path = "a/./b" }, "not relative"},
		{"absolute", func(r *Record) { r.Path = "/a/b" }, "not relative"},
		{"empty element", func(r *Record) { r.Path = "a//b" }, "not relative"},
		{"trailing slash", func(r *Record) { r.Path = "a/b/" }, "not relative"},
		{"backslash", func(r *Record) { r.Path = `a\b` }, "backslash"},
		{"invalid UTF-8", func(r *Record) { r.Path = "a/\xff.fit" }, "not valid UTF-8"},
		{"negative size", func(r *Record) { r.Size = -1 }, "size -1 is negative"},
		{"zero mtime", func(r *Record) { r.MTime = time.Time{} }, "mtime is missing"},
		{"zero at", func(r *Record) { r.At = time.Time{} }, "at is missing"},
		{"short sha256", func(r *Record) { r.SHA256 = shaA[:63] }, "not 64 lowercase hex"},
		{"long sha256", func(r *Record) { r.SHA256 = shaA + "aa" }, "not 64 lowercase hex"},
		{"uppercase sha256", func(r *Record) { r.SHA256 = strings.ToUpper(shaA) }, "not 64 lowercase hex"},
		{"non-hex sha256", func(r *Record) { r.SHA256 = "g" + shaA[1:] }, "not 64 lowercase hex"},
		{"empty how", func(r *Record) { r.How = "" }, `how "" is not`},
		{"unknown how", func(r *Record) { r.How = "verified" }, `how "verified" is not`},
		{"year past 9999", func(r *Record) { r.MTime = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }, "encode state record"},
		{"line too long", func(r *Record) { r.Path = strings.Repeat("p", maxLine) }, "more than 1048576"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, dest := dirs(t)
			s := mustOpen(t, stateDir, dest, nil)
			rec := valid
			tc.edit(&rec)
			err := s.Append(rec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Append = %v, want an error containing %q", err, tc.want)
			}
			if n := len(s.Snapshot()); n != 0 {
				t.Errorf("%d records after a refused Append, want 0", n)
			}
			p, _ := Path(stateDir, dest)
			if got := readFile(t, p); got != headerLine(dest) {
				t.Errorf("state file = %q after a refused Append, want only the header", got)
			}
		})
	}
	// The valid record itself is accepted, with each how.
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	for _, how := range []How{Copied, Replaced, Adopted} {
		rec := valid
		rec.How = how
		if err := s.Append(rec); err != nil {
			t.Errorf("Append with how %q: %v", how, err)
		}
	}
}

func TestAppendSyncFailure(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	p, _ := Path(stateDir, dest)
	failed := m31Copied(t)
	var sizes []int64
	var sawLine bool
	hookSync(t, func(f *os.File) error {
		fi, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, fi.Size())
		if len(sizes) > 1 {
			return nil
		}
		// The line is written before the fsync that makes it count.
		sawLine = strings.Contains(readFile(t, p), failed.SHA256)
		return errors.New("disk on fire")
	})
	if err := s.Append(failed); err == nil || !strings.Contains(err.Error(), "disk on fire") || strings.Contains(err.Error(), "undoing") {
		t.Fatalf("Append = %v, want only the fsync error", err)
	}
	if !sawLine {
		t.Error("the line was not in the file when fsync ran")
	}
	// The cut back to the header is fsynced too.
	if hdr := int64(len(headerLine(dest))); !slices.Equal(sizes, []int64{hdr + int64(len(lineM31Copied)), hdr}) {
		t.Errorf("file sizes at each fsync = %v, want the line then the header alone (%d)", sizes, hdr)
	}
	if _, ok := s.Lookup(failed.Path); ok {
		t.Error("a record whose fsync failed is in the state")
	}
	if got := readFile(t, p); got != headerLine(dest) {
		t.Errorf("after the failed Append the file is %q, want only the header", got)
	}

	next := Record{Path: "Autorun/Dark/d.fit", Size: 0, MTime: mustTime(t, "2026-10-29T01:00:00Z"), SHA256: shaA, At: mustTime(t, "2026-11-02T01:02:03Z"), How: Copied}
	if err := s.Append(next); err != nil {
		t.Fatalf("Append after a failed fsync: %v", err)
	}
	s.Close()
	if got, want := readFile(t, p), headerLine(dest)+lineDark; got != want {
		t.Errorf("state file =\n%s\nwant\n%s", got, want)
	}
}

func TestAppendUndoSyncFailureStops(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	p, _ := Path(stateDir, dest)
	errAppend, errUndo := errors.New("disk on fire"), errors.New("still on fire")
	calls := 0
	hookSync(t, func(*os.File) error {
		calls++
		if calls == 1 {
			return errAppend
		}
		return errUndo
	})
	rec := m31Copied(t)
	err := s.Append(rec)
	if !errors.Is(err, errAppend) || !errors.Is(err, errUndo) || !strings.Contains(err.Error(), "disk on fire; undoing it failed too: still on fire") {
		t.Fatalf("Append = %v, want the fsync error and the failed undo", err)
	}
	if got := readFile(t, p); got != headerLine(dest) {
		t.Errorf("after the failed Append the file is %q, want only the header", got)
	}
	syncFile = (*os.File).Sync
	if err := s.Append(rec); !errors.Is(err, errUndo) || !strings.Contains(err.Error(), "after an earlier failure") {
		t.Errorf("Append after a failed undo = %v, want a refusal", err)
	}
	if _, ok := s.Lookup(rec.Path); ok {
		t.Error("a refused record is in the state")
	}
}

func TestAppendWriteFailureStops(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	p, _ := Path(stateDir, dest)
	// A read-only handle makes both the write and its undoing fail.
	ro, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	rw := s.f
	s.f = ro
	defer rw.Close()

	rec := m31Copied(t)
	if err := s.Append(rec); err == nil || !strings.Contains(err.Error(), "append to state file") || !strings.Contains(err.Error(), "; undoing it failed too: ") {
		t.Fatalf("Append through a read-only handle = %v, want a write error and a failed undo", err)
	}
	if _, ok := s.Lookup(rec.Path); ok {
		t.Error("a record whose write failed is in the state")
	}
	s.f = rw
	if err := s.Append(rec); err == nil || !strings.Contains(err.Error(), "after an earlier failure") {
		t.Errorf("Append after an undone failure = %v, want a refusal", err)
	}
	s.f = ro
}

func TestAppendClosed(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	s.Close()
	if err := s.Append(m31Copied(t)); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Append after Close = %v, want a closed error", err)
	}
}

func TestAppendConcurrent(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	const workers, each = 8, 25
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				rec := Record{Path: fmt.Sprintf("w%d/f%d.fit", w, i), Size: int64(i), MTime: time.Unix(int64(i)+1, 0), SHA256: shaA, At: time.Unix(1, 0), How: Copied}
				if err := s.Append(rec); err != nil {
					t.Errorf("Append %s: %v", rec.Path, err)
				}
				if _, ok := s.Lookup(rec.Path); !ok {
					t.Errorf("Lookup %s after its Append found nothing", rec.Path)
				}
			}
		})
	}
	wg.Wait()
	s.Close()
	log, logs := logBuffer()
	s = mustOpen(t, stateDir, dest, log)
	if n := len(s.Snapshot()); n != workers*each {
		t.Errorf("reloaded %d records, want %d", n, workers*each)
	}
	if logs.Len() != 0 {
		t.Errorf("reload logged %q, want nothing", logs.String())
	}
}
