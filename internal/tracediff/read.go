// Package tracediff compares bounded, redacted decision traces offline. Equality
// covers only the retained projection, never erased bytes or unobserved events.
package tracediff

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

// Limits bounds each input separately and the combined encoded report. Zero
// selects the default; negative values are invalid. Actions counts all actions,
// not just the largest transaction. Bytes includes unknown JSON fields.
type Limits struct {
	Bytes, LineBytes, Records, Transactions, Actions, OutputBytes int
}

func defaults(l Limits) Limits {
	for _, p := range []struct {
		value    *int
		fallback int
	}{
		{&l.Bytes, tracerecord.SizeCap}, {&l.LineBytes, tracerecord.MaxLine},
		{&l.Records, 65536}, {&l.Transactions, 32768},
		{&l.Actions, 65536}, {&l.OutputBytes, 1 << 20},
	} {
		if *p.value == 0 {
			*p.value = p.fallback
		}
	}
	return l
}

// LimitError identifies the exhausted resource without exposing input text.
type LimitError struct {
	Resource string
	Limit    int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("trace diff: %s limit %d exceeded", e.Resource, e.Limit)
}

// InvalidError rejects malformed, torn, or structurally inconsistent records.
// Record is the one-based file position, or zero for an empty input.
type InvalidError struct{ Record int }

func (e *InvalidError) Error() string { return fmt.Sprintf("trace diff: invalid record %d", e.Record) }

type trace struct {
	steps                         []tracerecord.Record
	incomplete, loss, unavailable bool
}

// Pointers distinguish absent required fields from legitimate zero counters and
// clocks. JSON v2 owns parsing; unknown fields remain forward compatible.
type envelope struct {
	Version    int                     `json:"version"`
	Record     string                  `json:"record"`
	Sequence   *uint64                 `json:"sequence"`
	Machine    string                  `json:"machine"`
	Instance   *uint64                 `json:"instance"`
	MonoNS     *core.Instant           `json:"mono_ns"`
	Input      *summaryEnvelope        `json:"input"`
	Outputs    []*summaryEnvelope      `json:"outputs"`
	Dropped    *uint64                 `json:"dropped"`
	DropCauses *tracerecord.DropCauses `json:"drop_causes"`
}

// Kind is mandatory even for kind zero; null/absence cannot fabricate Start.
type summaryEnvelope struct {
	tracerecord.Summary
	Kind *uint8 `json:"kind"`
}

func (s *summaryEnvelope) value() tracerecord.Summary {
	result := s.Summary
	if s.Kind != nil {
		result.Kind = *s.Kind
	}
	return result
}

func read(in io.Reader, l Limits) (trace, error) {
	var t trace
	// Decode only after enforcing the total byte budget, including unknown fields.
	data, err := io.ReadAll(io.LimitReader(in, int64(l.Bytes)+1))
	if len(data) > l.Bytes {
		return t, &LimitError{"bytes", l.Bytes}
	}
	if err != nil {
		return t, &InvalidError{}
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	s.Buffer(make([]byte, min(4096, l.LineBytes)), l.LineBytes)
	var previous, dropped uint64
	n, actions := 0, 0
	ended := false
	for s.Scan() {
		n++
		if n > l.Records {
			return t, &LimitError{"records", l.Records}
		}
		var e envelope
		if json.Unmarshal(s.Bytes(), &e) != nil || ended || e.Version != 1 || e.Record == "" || e.Sequence == nil || e.Dropped == nil {
			return t, &InvalidError{n}
		}
		if *e.Sequence <= previous || *e.Dropped < dropped {
			return t, &InvalidError{n}
		}
		if *e.Sequence-previous != 1 || *e.Dropped != 0 || (e.DropCauses != nil && *e.DropCauses != (tracerecord.DropCauses{})) {
			t.loss = true
		}
		previous, dropped = *e.Sequence, *e.Dropped
		switch e.Record {
		case "end":
			if (e.Input != nil && e.Input.value() != (tracerecord.Summary{})) || len(e.Outputs) != 0 || e.Machine != "" {
				return t, &InvalidError{n}
			}
			ended = true
		case "step":
			if e.Input == nil || e.Input.Kind == nil || e.Instance == nil || e.MonoNS == nil || e.Machine == "" {
				return t, &InvalidError{n}
			}
			if len(t.steps) >= l.Transactions {
				return t, &LimitError{"transactions", l.Transactions}
			}
			if len(e.Outputs) > l.Actions-actions {
				return t, &LimitError{"actions", l.Actions}
			}
			outputs := make([]tracerecord.Summary, len(e.Outputs))
			for i, v := range e.Outputs {
				if v == nil || v.Kind == nil {
					return t, &InvalidError{n}
				}
				outputs[i] = v.value()
			}
			actions += len(e.Outputs)
			t.steps = append(t.steps, tracerecord.Record{Sequence: previous, Machine: e.Machine, Instance: *e.Instance, MonoNS: *e.MonoNS, Input: e.Input.value(), Outputs: outputs})
		default:
			t.unavailable = true
		}
	}
	if errors.Is(s.Err(), bufio.ErrTooLong) {
		return t, &LimitError{"line bytes", l.LineBytes}
	}
	if s.Err() != nil {
		return t, &InvalidError{n + 1}
	}
	if n == 0 {
		return t, &InvalidError{}
	}
	t.incomplete = !ended
	return t, nil
}
