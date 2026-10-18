package tracediff

import (
	"bytes"
	"errors"
	"github.com/go-json-experiment/json"
	"os"
	"strings"
	"testing"
	"time"
)

func compareTest(t *testing.T, left, right string, opts Options) report {
	t.Helper()
	b, err := Compare(strings.NewReader(left), strings.NewReader(right), opts)
	if err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(b, &r, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIdentical(t *testing.T) {
	r := compareTest(t, literal, literal, Options{})
	if r.Status != "equal" || len(r.Entries) != 0 {
		t.Fatalf("got %+v, want equal projection", r)
	}
	if r.Scope != "retained projection only; erased bytes unavailable" {
		t.Fatalf("scope: %q", r.Scope)
	}
}

func TestOpcodeOrdinal(t *testing.T) {
	right := strings.NewReplacer(`"type":16`, `"type":32`, `"type":32`, `"type":16`).Replace(literal)
	r := compareTest(t, literal, right, Options{})
	want := []entry{
		{Status: "different", Machine: "iotc", Instance: 1, Left: 1, Right: 1, Ordinal: 0, Fields: []string{"type"}},
		{Status: "different", Machine: "iotc", Instance: 1, Left: 1, Right: 1, Ordinal: 1, Fields: []string{"type"}},
	}
	a, _ := json.Marshal(r.Entries)
	b, _ := json.Marshal(want)
	if r.Status != "different" || !bytes.Equal(a, b) {
		t.Fatalf("got %s %+v, want %s", r.Status, r.Entries, b)
	}
}

func TestTimeTolerance(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement, field string }{
		{"clock", `"mono_ns":200`, `"mono_ns":211`, "mono_ns"},
		{"timer", `"at":150`, `"at":161`, "at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			right := strings.Replace(literal, tc.old, tc.replacement, 1)
			r := compareTest(t, literal, right, Options{Tolerance: 10 * time.Nanosecond})
			if r.Status != "different" || len(r.Entries) != 1 || len(r.Entries[0].Fields) != 1 || r.Entries[0].Fields[0] != tc.field {
				t.Fatalf("outside: %+v", r)
			}
			if r := compareTest(t, literal, right, Options{Tolerance: 11 * time.Nanosecond}); r.Status != "equal" {
				t.Fatalf("at tolerance: %+v", r)
			}
		})
	}
}

func TestNormalizeEqualityAndCausality(t *testing.T) {
	right := strings.NewReplacer(`"peer7"`, `"renamed-A"`, `"peer8"`, `"renamed-B"`, `"instance":7`, `"instance":99`, `"token":41`, `"token":1041`, `"token":42`, `"token":1042`, `"attempt":9`, `"attempt":75`, `"mono_ns":100`, `"mono_ns":1100`, `"mono_ns":200`, `"mono_ns":1200`, `"at":150`, `"at":1150`, `"utc":"ignored"`, `"utc":"different"`).Replace(literal)
	if r := compareTest(t, literal, right, Options{}); r.Status != "equal" {
		t.Fatalf("renamed: %+v", r)
	}
	for _, tc := range []struct{ name, before, after string }{
		{"peer merge", `"renamed-B"`, `"renamed-A"`},
		{"counter merge", `"token":1042`, `"token":1041`},
		{"wrong completion", `"kind":2,"token":1041`, `"kind":2,"token":1042`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := compareTest(t, literal, strings.Replace(right, tc.before, tc.after, 1), Options{}); r.Status == "equal" {
				t.Fatalf("lost equality relation: %+v", r)
			}
		})
	}
}

func TestInconclusiveEvidence(t *testing.T) {
	for _, tc := range []struct{ name, right string }{
		{"missing end", literal[:strings.LastIndex(literal, `{"version":1,"record":"end"`)]},
		{"drops", strings.ReplaceAll(literal, `"dropped":0`, `"dropped":1`)},
		{"overflow", strings.ReplaceAll(literal, `"peer7"`, `"peerOverflow"`)},
		{"port overflow", strings.Replace(literal, `"peer":"peer7"`, `"port_alias":"portOverflow"`, 1)},
		{"unsupported", strings.Replace(literal, `"kind":2`, `"kind":2,"unsupported":true`, 1)},
		{"sequence gap", strings.Replace(literal, `"sequence":3`, `"sequence":4`, 1)},
		{"drop cause", strings.Replace(literal, `"record":"end"`, `"record":"end","drop_causes":{"storage":1}`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, pair := range [][2]string{{literal, tc.right}, {tc.right, literal}} {
				r := compareTest(t, pair[0], pair[1], Options{})
				if r.Status != "inconclusive" || len(r.Entries) == 0 {
					t.Fatalf("got %+v, want inconclusive", r)
				}
			}
		})
	}
}

func TestUnmatchedAndUnavailable(t *testing.T) {
	right := strings.Replace(literal, `"outputs":[]`, `"outputs":[{"kind":4}]`, 1)
	r := compareTest(t, literal, right, Options{})
	if r.Status != "different" || len(r.Entries) != 1 || r.Entries[0].Status != "unmatched" || r.Entries[0].Left != 0 || r.Entries[0].Right != 2 || r.Entries[0].Ordinal != 0 {
		t.Fatalf("unmatched action: %+v", r)
	}
	// Explicit source capability: a capture cannot observe internal iotc Steps.
	end := `{"version":1,"record":"end","sequence":1,"dropped":0}` + "\n"
	r = compareTest(t, literal, end, Options{RightUnavailable: []string{"iotc"}})
	if r.Status != "inconclusive" || len(r.Entries) != 4 {
		t.Fatalf("unavailable: %+v", r)
	}
	for _, e := range r.Entries {
		if e.Status != "unavailable" {
			t.Fatalf("fabricated observation: %+v", e)
		}
	}
}

func TestDiffOutputBoundAndRedaction(t *testing.T) {
	right := strings.ReplaceAll(literal, `"peer7"`, `"SECRET_UID_KEY_PAYLOAD"`)
	right = strings.Replace(right, `"kind":2`, `"kind":3,"error":"SECRET_SERIAL"`, 1)
	for _, opts := range []Options{{}, {Limits: Limits{OutputBytes: 1}}} {
		b, err := Compare(strings.NewReader(literal), strings.NewReader(right), opts)
		if opts.Limits.OutputBytes != 0 {
			var e *LimitError
			if !errors.As(err, &e) || e.Resource != "output bytes" || b != nil {
				t.Fatalf("got %q %v, want typed output limit", b, err)
			}
		} else {
			if err != nil || bytes.Contains(b, []byte("SECRET")) {
				t.Fatalf("unsafe report: %s %v", b, err)
			}
			second, err := Compare(strings.NewReader(literal), strings.NewReader(right), opts)
			if err != nil || !bytes.Equal(b, second) {
				t.Fatal("report is nondeterministic")
			}
		}
	}
}

func TestCheckedInSyntheticTraces(t *testing.T) {
	for _, path := range []string{"../rdt/testdata/trace-ordered.jsonl", "../rdt/testdata/trace-loss.jsonl", "../iotc/testdata/trace-direct.jsonl", "../edge/testdata/trace-adapter.jsonl"} {
		t.Run(path, func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if r := compareTest(t, string(b), string(b), Options{}); r.Status != "equal" {
				t.Fatalf("fixture: %+v", r)
			}
		})
	}
}
