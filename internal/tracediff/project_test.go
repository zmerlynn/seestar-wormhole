package tracediff

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

func TestProjectionMetadata(t *testing.T) {
	a := tracerecord.Summary{Kind: 1, Length: 20, Type: 65, Version: 5, Transmissions: 1}
	b := a
	b.Length = 21
	b.Sequence.Low = 1
	b.Version = 6
	b.Transmissions = 2
	got, err := fields(a, b, 0, 0, new(correlations), new(aliases), 0)
	want := []string{"length", "wire_sequence", "wire_version", "transmissions"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v, want %v", got, err, want)
	}
}

func TestClockArithmetic(t *testing.T) {
	if within(core.Instant(math.MinInt64), core.Instant(math.MaxInt64), time.Duration(math.MaxInt64)) {
		t.Fatal("clock difference overflowed")
	}
	if !within(-11, -1, 10) || within(-11, 0, 10) {
		t.Fatal("incorrect signed tolerance")
	}
	for _, pair := range [][2]core.Instant{{math.MinInt64, 1}, {math.MaxInt64, -1}} {
		if _, err := relative(pair[0], pair[1]); err == nil {
			t.Fatal("relative clock overflow accepted")
		}
	}
	if got, err := relative(-10, 5); err != nil || got != -15 {
		t.Fatalf("regressing clock: %d %v", got, err)
	}
}

func TestCounterNamespaces(t *testing.T) {
	var ids correlations
	p := new(aliases)
	a := tracerecord.Summary{Token: 1, Attempt: 1, Channel: 1, Generation: 1, Request: 1, PortAlias: "port1"}
	b := tracerecord.Summary{Token: 2, Attempt: 3, Channel: 4, Generation: 5, Request: 6, PortAlias: "port99"}
	if f, err := fields(a, b, 0, 0, &ids, p, 0); err != nil || len(f) != 0 {
		t.Fatalf("namespaces: %v %v", f, err)
	}
	b.Request = 7
	if f, err := fields(a, b, 0, 0, &ids, p, 0); err != nil || !reflect.DeepEqual(f, []string{"request"}) {
		t.Fatalf("causal reference: %v %v", f, err)
	}
}
