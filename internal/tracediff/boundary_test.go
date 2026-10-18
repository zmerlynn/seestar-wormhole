package tracediff

import (
	"bytes"
	"errors"
	"github.com/go-json-experiment/json"
	"math"
	"strings"
	"testing"
)

func TestUnmatchedTransactionAlignment(t *testing.T) {
	extra := `{"version":1,"record":"step","sequence":2,"machine":"iotc","instance":7,"mono_ns":125,"input":{"kind":8},"dropped":0}` + "\n"
	right := strings.Replace(literal, `{"version":1,"record":"step","sequence":2`, extra+`{"version":1,"record":"step","sequence":3`, 1)
	right = strings.Replace(right, `"record":"end","sequence":3`, `"record":"end","sequence":4`, 1)
	for _, pair := range [][2]string{{literal, right}, {right, literal}} {
		r := compareTest(t, pair[0], pair[1], Options{})
		if r.Status != "different" || len(r.Entries) != 1 || r.Entries[0].Status != "unmatched" || r.Entries[0].Ordinal != -1 {
			t.Fatalf("alignment: %+v", r)
		}
	}
}

func TestInstanceSchedulingAndScope(t *testing.T) {
	// Distinct machine clocks, reused counter values in separate instances, and
	// a different interleaving must preserve the same per-instance decisions.
	left := `{"version":1,"record":"step","sequence":1,"machine":"rdt","instance":1,"mono_ns":-20,"input":{"kind":0,"token":1},"dropped":0}
{"version":1,"record":"step","sequence":2,"machine":"iotc","instance":2,"mono_ns":1000,"input":{"kind":0,"token":1},"dropped":0}
{"version":1,"record":"step","sequence":3,"machine":"rdt","instance":1,"mono_ns":-10,"input":{"kind":1,"token":1},"dropped":0}
{"version":1,"record":"end","sequence":4,"dropped":0}
`
	right := `{"version":1,"record":"step","sequence":1,"machine":"iotc","instance":99,"mono_ns":5,"input":{"kind":0,"token":40},"dropped":0}
{"version":1,"record":"step","sequence":2,"machine":"rdt","instance":98,"mono_ns":200,"input":{"kind":0,"token":20},"dropped":0}
{"version":1,"record":"step","sequence":3,"machine":"rdt","instance":98,"mono_ns":210,"input":{"kind":1,"token":20},"dropped":0}
{"version":1,"record":"end","sequence":4,"dropped":0}
`
	if r := compareTest(t, left, right, Options{}); r.Status != "equal" {
		t.Fatalf("scheduling: %+v", r)
	}
	// A different partition into instances cannot be hidden by renumbering.
	right = strings.Replace(right, `"instance":98,"mono_ns":210`, `"instance":97,"mono_ns":210`, 1)
	if r := compareTest(t, left, right, Options{}); r.Status == "equal" {
		t.Fatalf("instance merge: %+v", r)
	}
}

func TestCompareRejectsUnsafeOptionsAndInput(t *testing.T) {
	for _, o := range []Options{
		{Tolerance: -1}, {Limits: Limits{Bytes: -1}}, {Limits: Limits{Bytes: math.MaxInt}},
		{Limits: Limits{Records: -1}}, {Limits: Limits{Actions: -1}},
		{Limits: Limits{Transactions: -1}}, {Limits: Limits{LineBytes: -1}},
		{Limits: Limits{OutputBytes: -1}}, {LeftUnavailable: []string{"SECRET"}},
	} {
		b, err := Compare(strings.NewReader(literal), strings.NewReader(literal), o)
		if err == nil || b != nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("options: %q %v", b, err)
		}
	}
	for _, side := range []bool{false, true} {
		a, b := literal, "{SECRET"
		if side {
			a, b = b, a
		}
		out, err := Compare(strings.NewReader(a), strings.NewReader(b), Options{})
		var invalid *InvalidError
		if out != nil || !errors.As(err, &invalid) || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("input: %q %v", out, err)
		}
	}
}

func TestForwardCompatibilityAndUnknownSourceRedaction(t *testing.T) {
	right := strings.Replace(literal, `"kind":0`, `"kind":0,"future":{"secret":"SECRET"}`, 1)
	if r := compareTest(t, literal, right, Options{}); r.Status != "equal" {
		t.Fatalf("unknown safe JSON field: %+v", r)
	}
	future := strings.Replace(literal, `"record":"step"`, `"record":"SECRET"`, 1)
	if r := compareTest(t, literal, future, Options{}); r.Status != "inconclusive" {
		t.Fatalf("unknown comparison unit: %+v", r)
	}
	right = strings.ReplaceAll(right, `"machine":"iotc"`, `"machine":"SECRET"`)
	b, err := Compare(strings.NewReader(literal), strings.NewReader(right), Options{})
	if err != nil || bytes.Contains(b, []byte("SECRET")) {
		t.Fatalf("unknown source leak: %s %v", b, err)
	}
	var r report
	if json.Unmarshal(b, &r, json.MatchCaseInsensitiveNames(true)) != nil || r.Status != "inconclusive" {
		t.Fatalf("unknown source: %s", b)
	}
}

func FuzzCompare(f *testing.F) {
	f.Add([]byte(literal))
	f.Add([]byte("{SECRET"))
	f.Add([]byte("null\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		out, err := Compare(bytes.NewReader(b), bytes.NewReader(b), Options{Limits: Limits{Bytes: 8192, LineBytes: 4096, Records: 32, Transactions: 16, Actions: 32, OutputBytes: 8192}})
		if err != nil {
			if out != nil {
				t.Fatal("partial report on error")
			}
			return
		}
		var r report
		if json.Unmarshal(out, &r, json.MatchCaseInsensitiveNames(true)) != nil || r.Status == "different" {
			t.Fatalf("self comparison: %s", out)
		}
	})
}
