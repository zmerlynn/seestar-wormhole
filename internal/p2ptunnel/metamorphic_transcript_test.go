package p2ptunnel

// This deliberately retains v1 as the comparator for immutable v1 owner fixtures;
// nil slices and byte arrays in those fixtures use the v1 representation.
import (
	"bytes"
	"compress/gzip"
	jsonv1 "encoding/json"
	"os"
	"reflect"
	"testing"
)

type metamorphicTurn struct {
	Event   Event
	Actions []Action
}
type metamorphicTranscript struct {
	Name    string
	Variant bool
	Turns   []metamorphicTurn
}

func metamorphicTranscriptRun(t *testing.T, row metamorphicFixture, variant bool) metamorphicTranscript {
	trace := metamorphicTranscript{Name: row.Name, Variant: variant}
	wire := unhex(t, row.Wire)
	var unknown []byte
	if variant {
		wire = unhex(t, row.VariantWire)
		if row.Phase == Established {
			unknown = append(unhex(t, "fe630f00"), bytes.Repeat([]byte{0xa5}, 15)...)
		}
	}
	observe := func(e Event, a []Action) {
		b, err := jsonv1.Marshal(metamorphicTurn{e, a})
		if err != nil {
			t.Fatal(err)
		}
		var turn metamorphicTurn
		if err = jsonv1.Unmarshal(b, &turn); err != nil {
			t.Fatal(err)
		}
		trace.Turns = append(trace.Turns, turn)
	}
	metamorphicRun(t, row, wire, unknown, 0, observe)
	return trace
}
func TestMetamorphicOwnerTranscripts(t *testing.T) {
	file, err := os.Open("testdata/metamorphic-transcripts.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zipped, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer zipped.Close()
	var traces []metamorphicTranscript
	if err = jsonv1.NewDecoder(zipped).Decode(&traces); err != nil {
		t.Fatal(err)
	}
	rows := metamorphicFixtures(t)
	if len(traces) != len(rows)*2 {
		t.Fatal("incomplete owner transcript catalog")
	}
	for i, trace := range traces {
		got := metamorphicTranscriptRun(t, rows[i/2], i%2 == 1)
		if !reflect.DeepEqual(got, trace) {
			t.Fatalf("%s immutable owner transcript differs", trace.Name)
		}
	}
}
