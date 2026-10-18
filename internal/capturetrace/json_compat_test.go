package capturetrace

import (
	"bytes"
	jsonv1 "encoding/json"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"testing"
)

// V1 is solely the byte comparator for the capture projection's JSONL output.
func TestCaptureJSONV1Bytes(t *testing.T) {
	d := decoder{limits: Limits{OutputBytes: 4096}}
	r := tracerecord.Record{Version: 1, Record: "step", Sequence: 1, Machine: "iotc", Input: tracerecord.Summary{Kind: 1}, Outputs: []tracerecord.Summary{{Kind: 2}}}
	if err := d.append(r); err != nil {
		t.Fatal(err)
	}
	got, err := d.finish(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(got.JSONL), []byte{'\n'}) {
		var record tracerecord.Record
		if err := jsonv1.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		want, err := jsonv1.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(line, want) {
			t.Fatalf("capture=%s; v1=%s", line, want)
		}
	}
}
