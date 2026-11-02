package state

import (
	"bytes"
	"io"
	"log/slog"
	"maps"
	"testing"
)

// FuzzLoad checks that the loader never panics, that what it keeps is a
// clean prefix of the file, and that the prefix loads to the same records.
func FuzzLoad(f *testing.F) {
	const dest = "/d"
	hdr := headerLine(dest)
	for _, seed := range []string{
		"",
		hdr,
		hdr + lineM31Copied + lineDark + lineM31Replaced + lineM31Adopted,
		hdr + lineM31Copied + `{"path":"a","si`,
		hdr + lineM31Copied + "{nope\n",
		hdr + "{nope\n" + lineDark,
		hdr + "\n",
		hdr + `{"path":"a/b","size":1,"mtime":"2026-10-29T05:31:19.25+02:00","how":"x","more":[{}]}` + "\n",
		hdr + `{"path":"../a","size":-1,"mtime":"0001-01-01T00:00:00Z"}` + "\n",
		hdr + "null\n[]\n",
		`{"v":2,"dest":"/d"}` + "\n",
		`{"v":1,"dest":"/e"}` + "\n",
		`{"v":1,"dest":"/d"}`,
	} {
		f.Add([]byte(seed))
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		records, end, torn, err := load(bytes.NewReader(data), dest, log)
		if err != nil {
			return
		}
		if end < int64(len(hdr)) || end > int64(len(data)) {
			t.Fatalf("end = %d, want within [%d, %d]", end, len(hdr), len(data))
		}
		if torn == nil && end != int64(len(data)) {
			t.Fatalf("end = %d with nothing dropped, want %d", end, len(data))
		}
		if data[end-1] != '\n' {
			t.Fatalf("the kept prefix does not end in a newline")
		}
		for p, rec := range records {
			if p != rec.Path {
				t.Fatalf("record keyed %q has path %q", p, rec.Path)
			}
			if err := checkLoaded(rec); err != nil {
				t.Fatalf("loaded an invalid record: %v", err)
			}
		}
		again, end2, torn2, err := load(bytes.NewReader(data[:end]), dest, log)
		if err != nil || torn2 != nil || end2 != end {
			t.Fatalf("reloading the kept prefix: end=%d torn=%v err=%v, want end=%d and no error", end2, torn2, err, end)
		}
		if !maps.EqualFunc(records, again, func(a, b Record) bool {
			return a.Path == b.Path && a.Size == b.Size && a.MTime.Equal(b.MTime) && a.SHA256 == b.SHA256 && a.At.Equal(b.At) && a.How == b.How
		}) {
			t.Fatalf("the kept prefix loads to other records")
		}
	})
}
