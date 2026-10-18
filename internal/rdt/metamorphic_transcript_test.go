package rdt

// This deliberately retains v1 as the comparator for immutable v1 owner fixtures;
// nil slices and byte arrays in those fixtures use the v1 representation.
import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	jsonv1 "encoding/json"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"os"
	"reflect"
	"testing"
)

type metamorphicTurn struct {
	Event   Event
	Now     core.Instant
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
		// Literal fixed mutation, independent of the variable-noise fuzz path.
		wire[18], wire[19] = 0xa5, 0xa5
		if Type(wire[4]) == SACK2 {
			copy(wire[20:24], []byte{0xa5, 0xa5, 0xa5, 0xa5})
			wire = append(wire[:44], bytes.Repeat([]byte{0xa5}, 15)...)
			binary.LittleEndian.PutUint16(wire[6:], uint16(len(wire)-20))
		}
		unknown = unhex(t, "5a97c2f144050100123456789abcdef003feffffa5")
	}
	observe := func(e Event, now core.Instant, a []Action) {
		b, err := jsonv1.Marshal(metamorphicTurn{e, now, a})
		if err != nil {
			t.Fatal(err)
		}
		var turn metamorphicTurn
		if err = jsonv1.Unmarshal(b, &turn); err != nil {
			t.Fatal(err)
		}
		trace.Turns = append(trace.Turns, turn)
	}
	metamorphicRun(t, wire, unknown, observe)
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

func TestMetamorphicUnknownBounds(t *testing.T) {
	for _, n := range []int{0, 1, MaxData, MaxData + 1} {
		wire := append(unhex(t, "5a97c2f144050000123456789abcdef003feffff"), bytes.Repeat([]byte{0xa5}, n)...)
		binary.LittleEndian.PutUint16(wire[6:], uint16(n))
		p, err := Decode(wire)
		if (err == nil) != (n <= MaxData) {
			t.Fatalf("unknown size=%d err=%v", n, err)
		}
		if err != nil {
			continue
		}
		m := newMachine(t, nil)
		ready(t, m)
		before := *m
		if a := m.Step(Event{Kind: Receive, Packet: p}, 0); len(metamorphicActions(t, a, 0)) != 0 || !reflect.DeepEqual(before, *m) {
			t.Fatal("unknown changed state/actions")
		}
		if _, err = p.Encode(); err == nil {
			t.Fatal("unknown receive form became sendable")
		}
	}
}
