package tracediff

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Literal synthetic decisions, independent of the recorder/JSON encoder.
const literal = `{"version":1,"record":"step","sequence":1,"machine":"iotc","instance":7,"mono_ns":100,"utc":"ignored","input":{"kind":0,"attempt":9},"outputs":[{"kind":1,"type":16,"token":41,"attempt":9,"peer":"peer7","at":150},{"kind":1,"type":32,"token":42,"attempt":9,"peer":"peer8"}],"dropped":0}
{"version":1,"record":"step","sequence":2,"machine":"iotc","instance":7,"mono_ns":200,"input":{"kind":2,"token":41,"attempt":9,"peer":"peer7"},"outputs":[],"dropped":0}
{"version":1,"record":"end","sequence":3,"dropped":0}
`

func TestReadBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limits   Limits
		resource string
	}{
		{"records", Limits{Records: 2}, "records"},
		{"transactions", Limits{Transactions: 1}, "transactions"},
		{"actions", Limits{Actions: 1}, "actions"},
		{"bytes", Limits{Bytes: 10}, "bytes"},
		{"line", Limits{LineBytes: 10}, "line bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := read(strings.NewReader(literal), defaults(tc.limits))
			var limit *LimitError
			if !errors.As(err, &limit) || limit.Resource != tc.resource {
				t.Fatalf("got %v, want typed %s limit", err, tc.resource)
			}
		})
	}
}

func TestReadRejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"", "null\n", "{\n", literal[:len(literal)-4],
		strings.Replace(literal, `"version":1`, `"version":2`, 1),
		strings.Replace(literal, `"sequence":2`, `"sequence":1`, 1),
		strings.Replace(literal, `"input":{"kind":0,"attempt":9}`, `"input":null`, 1),
		strings.Replace(literal, `"mono_ns":100,`, "", 1),
		literal + literal,
		strings.Replace(literal, `"outputs":[]`, `"outputs":[null]`, 1),
		strings.Replace(literal, `"kind":0,`, "", 1),
		strings.Replace(literal, `"kind":0`, `"kind":null`, 1),
		strings.Replace(literal, `"record":"step",`, "", 1),
		strings.Replace(literal, `"record":"step"`, `"record":null`, 1),
	} {
		_, err := read(strings.NewReader(s), defaults(Limits{}))
		var invalid *InvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("got %v, want typed invalid trace", err)
		}
	}
}

func TestReadSyntheticFixture(t *testing.T) {
	f, err := os.Open("../rdt/testdata/trace-ordered.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := read(f, defaults(Limits{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.incomplete || len(got.steps) == 0 {
		t.Fatal("complete fixture rejected")
	}
}
