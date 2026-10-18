package tracerecord

import (
	"bytes"
	jsonv1 "encoding/json"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"testing"
	"time"
)

// Decode with v1 and re-encode with v1 to independently check writer bytes.
func TestTraceJSONV1Bytes(t *testing.T) {
	sink := new(memorySink)
	w := New(sink, 4096, func() time.Time { return time.Unix(0, 0) })
	w.Record("rdt", 1, core.Instant(0), rdt.Event{Kind: rdt.Start}, []rdt.Action{{Kind: rdt.ArmTimer, At: 30}})
	w.Close()
	for _, line := range bytes.Split(bytes.TrimSpace(sink.Bytes()), []byte{'\n'}) {
		var record Record
		if err := jsonv1.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		want, err := jsonv1.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(line, want) {
			t.Fatalf("trace=%s; v1=%s", line, want)
		}
	}
}

// A present empty loss object differs from an absent pointer in the file schema.
func TestTraceJSONV1PointerOmission(t *testing.T) {
	for _, causes := range []*DropCauses{nil, {}, {Invalid: 1}} {
		record := Record{Version: 1, Record: "end", DropCauses: causes}
		got, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		want, err := jsonv1.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("trace=%s; v1=%s", got, want)
		}
	}
}
