package tracediff

import (
	"errors"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"math"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

// Options configures comparison. Tolerance is inclusive and must be nonnegative.
// Unavailable lists explicitly identify machines a source cannot observe (for
// example internal Steps absent from a packet capture). Omitting observations
// without declaring this capability produces unmatched observations instead.
type Options struct {
	Limits                            Limits
	Tolerance                         time.Duration
	LeftUnavailable, RightUnavailable []string
}

type entry struct {
	Status   string   `json:"status"`
	Machine  string   `json:"machine,omitempty"`
	Instance int      `json:"instance,omitzero"`
	Left     uint64   `json:"left_sequence,omitzero"`
	Right    uint64   `json:"right_sequence,omitzero"`
	Ordinal  int      `json:"ordinal"` // -1 is the input; actions start at zero.
	Fields   []string `json:"fields,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type report struct {
	Status  string  `json:"status"`
	Scope   string  `json:"scope"`
	Entries []entry `json:"entries"`
}

type comparison struct {
	report     report
	options    Options
	aliases    aliases
	outputSize int
	uncertain  bool
}

func (c *comparison) add(e entry) error {
	if e.Status == "inconclusive" || e.Status == "unavailable" {
		c.report.Status = "inconclusive"
	} else if c.report.Status == "equal" {
		c.report.Status = "different"
	}
	// Budget the same HTML/JS-escaped bytes emitted by Compare below.
	b, _ := json.Marshal(e, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	if len(b)+1 > c.options.Limits.OutputBytes-c.outputSize {
		return &LimitError{"output bytes", c.options.Limits.OutputBytes}
	}
	c.outputSize += len(b) + 1
	c.report.Entries = append(c.report.Entries, e)
	return nil
}

// Compare reads version-1 JSON Lines and returns a deterministic redacted JSON
// report. Missing end/loss taints the whole input: dropped transactions have no
// reliable machine or time attribution. Overflow/unsupported summaries taint
// their observation. An inconclusive report can still contain definite diffs.
// Malformed JSON (including a torn last record) returns InvalidError, with no
// partial report. LimitError likewise returns no partial report.
//
// Alignment pairs instances by machine/first use and transactions by input kind
// with a bounded 32-transaction lookahead. Ambiguous repeated kinds retain causal
// order; no optimal alignment or real-device conformance is claimed. Local
// counters and aliases use bijections established only at paired observations.
func Compare(left, right io.Reader, options Options) ([]byte, error) {
	options.Limits = defaults(options.Limits)
	l := options.Limits
	if options.Tolerance < 0 || l.Bytes < 1 || l.Bytes == math.MaxInt || l.LineBytes < 1 || l.Records < 1 || l.Transactions < 1 || l.Actions < 1 || l.OutputBytes < 1 {
		return nil, errors.New("trace diff: invalid options")
	}
	for _, list := range [][]string{options.LeftUnavailable, options.RightUnavailable} {
		for _, machine := range list {
			if !knownMachine(machine) {
				return nil, errors.New("trace diff: invalid unavailable machine")
			}
		}
	}
	a, err := read(left, l)
	if err != nil {
		return nil, err
	}
	b, err := read(right, l)
	if err != nil {
		return nil, err
	}
	c := comparison{options: options, report: report{Status: "equal", Scope: "retained projection only; erased bytes unavailable", Entries: []entry{}}}
	for _, v := range []struct {
		bad    bool
		reason string
	}{
		{a.incomplete, "left missing end"}, {b.incomplete, "right missing end"},
		{a.loss, "left loss"}, {b.loss, "right loss"},
		{a.unavailable, "left observation unavailable"}, {b.unavailable, "right observation unavailable"},
	} {
		if v.bad {
			c.uncertain = true
			if err := c.add(entry{Status: "inconclusive", Ordinal: -1, Reason: v.reason}); err != nil {
				return nil, err
			}
		}
	}
	bs := streams(b)
	used := make([]bool, len(bs))
	byMachine := make(map[string][]int)
	for j, s := range bs {
		byMachine[s.machine] = append(byMachine[s.machine], j)
	}
	offsets := make(map[string]int)
	instances := make(map[string]int)
	for _, as := range streams(a) {
		instances[as.machine]++
		var match stream
		candidates := byMachine[as.machine]
		offset := offsets[as.machine]
		if offset < len(candidates) {
			j := candidates[offset]
			offsets[as.machine]++
			used[j] = true
			match = bs[j]
		}
		if err := c.compareStream(as, match, instances[as.machine]); err != nil {
			return nil, err
		}
	}
	for j, s := range bs {
		if used[j] {
			continue
		}
		instances[s.machine]++
		if err := c.compareStream(stream{}, s, instances[s.machine]); err != nil {
			return nil, err
		}
	}
	// Preserve report escaping and struct order; omitzero retains scalar omissions.
	data, err := json.Marshal(c.report, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	if err != nil {
		return nil, err
	}
	if len(data) > l.OutputBytes {
		return nil, &LimitError{"output bytes", l.OutputBytes}
	}
	return data, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (c *comparison) compareStream(a, b stream, instance int) error {
	machine := a.machine
	if machine == "" {
		machine = b.machine
	}
	label := machine
	if !knownMachine(label) {
		label = "unknown"
	}
	base := entry{Machine: label, Instance: instance, Ordinal: -1}
	unavailable := !knownMachine(machine) || contains(c.options.LeftUnavailable, machine) || contains(c.options.RightUnavailable, machine)
	var ids correlations
	i, j := 0, 0
	for i < len(a.steps) || j < len(b.steps) {
		var ar, br *tracerecord.Record
		if i < len(a.steps) {
			ar = &a.steps[i]
		}
		if j < len(b.steps) {
			br = &b.steps[j]
		}
		// Only kinds anchor alignment. Changed opcodes/lengths/times stay at their
		// original ordinal and are compared, rather than disappearing as gaps.
		if ar != nil && br != nil && ar.Input.Kind != br.Input.Kind {
			leftSkip, rightSkip := nextKind(a.steps, i, br.Input.Kind), nextKind(b.steps, j, ar.Input.Kind)
			if leftSkip > 0 && (rightSkip == 0 || leftSkip <= rightSkip) {
				br = nil
			} else if rightSkip > 0 {
				ar = nil
			}
		}
		if ar != nil && br != nil {
			at, err := relative(ar.MonoNS, a.steps[0].MonoNS)
			if err != nil {
				return err
			}
			bt, err := relative(br.MonoNS, b.steps[0].MonoNS)
			if err != nil {
				return err
			}
			timing := !within(at, bt, c.options.Tolerance)
			for ordinal := -1; ordinal < max(len(ar.Outputs), len(br.Outputs)); ordinal++ {
				var av, bv *tracerecord.Summary
				if ordinal == -1 {
					av = &ar.Input
					bv = &br.Input
				} else {
					if ordinal < len(ar.Outputs) {
						av = &ar.Outputs[ordinal]
					}
					if ordinal < len(br.Outputs) {
						bv = &br.Outputs[ordinal]
					}
				}
				e := base
				e.Ordinal = ordinal
				if av != nil {
					e.Left = ar.Sequence
				}
				if bv != nil {
					e.Right = br.Sequence
				}
				if err := c.observation(e, av, bv, a.steps[0].MonoNS, b.steps[0].MonoNS, &ids, unavailable, timing && ordinal == -1); err != nil {
					return err
				}
			}
			i++
			j++
		} else {
			r := ar
			if r == nil {
				r = br
			}
			for ordinal := -1; ordinal < len(r.Outputs); ordinal++ {
				v := &r.Input
				if ordinal >= 0 {
					v = &r.Outputs[ordinal]
				}
				e := base
				e.Ordinal = ordinal
				var av, bv *tracerecord.Summary
				if ar != nil {
					e.Left = r.Sequence
					av = v
				} else {
					e.Right = r.Sequence
					bv = v
				}
				if err := c.observation(e, av, bv, 0, 0, &ids, unavailable, false); err != nil {
					return err
				}
			}
			if ar != nil {
				i++
			} else {
				j++
			}
		}
	}
	return nil
}

func nextKind(steps []tracerecord.Record, start int, kind uint8) int {
	for i := start + 1; i < len(steps) && i-start <= 32; i++ {
		if steps[i].Input.Kind == kind {
			return i - start
		}
	}
	return 0
}

func (c *comparison) observation(e entry, a, b *tracerecord.Summary, ao, bo core.Instant, ids *correlations, unavailable, timing bool) error {
	switch {
	case unavailable || (a != nil && a.Unsupported) || (b != nil && b.Unsupported):
		e.Status = "unavailable"
		e.Reason = "internal observation unavailable"
	case (a != nil && uncertain(*a)) || (b != nil && uncertain(*b)):
		e.Status = "inconclusive"
		e.Reason = "alias overflow"
	case a == nil || b == nil:
		e.Status = "unmatched"
	default:
		var err error
		e.Fields, err = fields(*a, *b, ao, bo, ids, &c.aliases, c.options.Tolerance)
		if err != nil {
			return err
		}
		if timing {
			e.Fields = append(e.Fields, "mono_ns")
		}
		if len(e.Fields) == 0 {
			if !c.uncertain {
				return nil
			}
			e.Status = "inconclusive"
			e.Reason = "incomplete evidence"
		} else {
			e.Status = "different"
		}
	}
	return c.add(e)
}
