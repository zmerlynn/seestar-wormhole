package iotc

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

// Transcripts retain every owner event and every resulting action, including
// complete wire/AAD/ciphertext/plaintext and synthetic key material. No traces
// are updated by tests. The gzip/JSON formats are decoded by standard libraries.
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
		row := rows[i/2]
		if trace.Name != row.Name || trace.Variant != (i%2 == 1) {
			t.Fatal("transcript identity differs")
		}
		var got []metamorphicTurn
		// Serializing at the observation boundary freezes borrowed event data before
		// future effects execute; JSON is also used for the immutable representation.
		observe := func(e Event, a []Action) {
			b, err := jsonv1.Marshal(metamorphicTurn{e, a})
			if err != nil {
				t.Fatal(err)
			}
			var turn metamorphicTurn
			if err = jsonv1.Unmarshal(b, &turn); err != nil {
				t.Fatal(err)
			}
			got = append(got, turn)
		}
		p, l := metamorphicPeer(t, row, UDP, observe)
		wire := unhex(row.Wire)
		if trace.Variant {
			wire = unhex(row.VariantWire)
		}
		actions := metamorphicIngress(t, p, l, wire)
		// Complete nonterminal crypto/send effects as the existing synthetic edge
		// does. Alert terminal actions need no completion.
		if row.Mode != "peer" && row.Mode != "alert" {
			p.run(actions)
		}
		if !reflect.DeepEqual(got, trace.Turns) {
			b, _ := jsonv1.Marshal(got)
			want, _ := jsonv1.Marshal(trace.Turns)
			t.Fatalf("%s variant=%v immutable owner transcript differs (got %d bytes, want %d; equal=%v)", trace.Name, trace.Variant, len(b), len(want), bytes.Equal(b, want))
		}
	}
}
