package tracerecord

import (
	"bytes"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
)

type memorySink struct{ bytes.Buffer }

func (*memorySink) Close() error { return nil }

func TestRecordAndCap(t *testing.T) {
	s := new(memorySink)
	w := New(s, 4096, func() time.Time { return time.Unix(0, 0) })
	for i := 0; i < 100; i++ {
		w.Record("rdt", 1, core.Instant(i), rdt.Event{Kind: rdt.Start}, []rdt.Action{{Kind: rdt.ArmTimer, At: 30}})
	}
	w.Close()
	if s.Len() > 4096 {
		t.Fatalf("size %d exceeds cap", s.Len())
	}
	records, err := Read(bytes.NewReader(s.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 2 || records[len(records)-1].Dropped == 0 || records[len(records)-1].Record != "end" {
		t.Fatalf("missing loss trailer: %+v", records)
	}
	for _, r := range records[:len(records)-1] {
		if r.UTC != "1970-01-01T00:00:00Z" || r.Machine != "rdt" || r.Instance != 1 || len(r.Outputs) != 1 {
			t.Fatalf("record %+v", r)
		}
	}
}
func TestPrivateExclusiveFile(t *testing.T) {
	path := t.TempDir() + "/trace.jsonl"
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("mode %v %v", info, err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("overwrote trace")
	}
}
func TestReaderBoundsAndUnknownFields(t *testing.T) {
	if _, err := Read(bytes.NewBufferString(`{"version":1,"record":"step","future":true}` + "\n")); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"version":99}`, `{`, string(bytes.Repeat([]byte{' '}, MaxLine+1))} {
		if _, err := Read(bytes.NewBufferString(input)); err == nil {
			t.Fatal("accepted invalid trace")
		}
	}
}
