package tracerecord

import (
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// Replay feeds trace inputs into a fresh machine using the recorded owner clock.
// hydrate supplies public fixture bytes and synthetic secrets erased by redaction;
// its projection must match the recorded input before Step is called. Configure
// the fresh machine with the scenario's explicit config and injected RNG first.
// Comparisons cover the complete redacted action projection, never erased bytes.
// Incomplete/lossy traces cannot prove replay and are rejected before any Step.
func Replay[E, A any](records []Record, machine string, instance uint64, hydrate func(Record) (E, error), step func(E, core.Instant) []A) error {
	if len(records) == 0 || records[len(records)-1].Record != "end" {
		return errors.New("trace: incomplete recording")
	}
	var previous uint64
	for _, r := range records {
		if r.Version != 1 || r.Dropped != 0 {
			return errors.New("trace: incompatible or lossy recording")
		}
		if r.Sequence != previous+1 {
			return errors.New("trace: sequence has a gap or duplicate")
		}
		previous = r.Sequence
	}
	redactor := &Writer{peers: make(map[string]string), ports: make(map[uint16]string), servicePorts: make(map[uint16]bool)}
	// Only mapped device destinations can produce a raw port in this format.
	// Synthetic resolvers supply aliased port values out of band, just like IPs.
	for _, r := range records {
		for _, s := range append([]Summary{r.Input}, r.Outputs...) {
			if s.Peer == "peerOverflow" || s.PortAlias == "portOverflow" {
				return errors.New("trace: alias capacity exceeded")
			}
			if s.Port != 0 {
				redactor.servicePorts[s.Port] = true
			}
		}
	}
	aliases := make(map[string]string)
	matched := false
	for _, r := range records {
		if r.Record == "end" {
			continue
		}
		if r.Record != "step" {
			return errors.New("trace: unknown comparison unit")
		}
		if r.Machine != machine || r.Instance != instance {
			continue
		}
		matched = true
		e, err := hydrate(r)
		if err != nil {
			return fmt.Errorf("trace: input %d: %w", r.Sequence, err)
		}
		gotInput := replayAlias(redactor.summarize(e), r.Input, aliases)
		if gotInput.Unsupported || gotInput != r.Input {
			return fmt.Errorf("trace: input %d projection got %+v want %+v", r.Sequence, gotInput, r.Input)
		}
		actions := step(e, r.MonoNS)
		if len(actions) != len(r.Outputs) {
			return fmt.Errorf("trace: step %d action count got %d want %d", r.Sequence, len(actions), len(r.Outputs))
		}
		for i, a := range actions {
			got := replayAlias(redactor.summarize(a), r.Outputs[i], aliases)
			if got.Unsupported || got != r.Outputs[i] {
				return fmt.Errorf("trace: step %d action %d got %+v want %+v", r.Sequence, i, got, r.Outputs[i])
			}
		}
	}
	if !matched {
		return errors.New("trace: machine instance not found")
	}
	return nil
}

// A selected instance can first encounter peer4 or port4 in a composed trace.
// Synthetic addresses/ports start at alias1; preserve equality relationships
// instead of depending on aliases introduced by other machine instances.
func replayAlias(got, want Summary, aliases map[string]string) Summary {
	got.Peer = normalizeAlias(got.Peer, want.Peer, aliases)
	got.PortAlias = normalizeAlias(got.PortAlias, want.PortAlias, aliases)
	return got
}
func normalizeAlias(got, want string, aliases map[string]string) string {
	if got == "" || want == "" {
		return got
	}
	if mapped, ok := aliases[got]; ok {
		return mapped
	}
	for _, mapped := range aliases {
		if mapped == want {
			return ""
		}
	}
	aliases[got] = want
	return want
}
