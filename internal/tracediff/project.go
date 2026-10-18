package tracediff

import (
	"math"
	"reflect"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

// Each namespace is a bijection: merging two identities or changing a completion
// to reference a different effect must remain observable after renumbering.
type identities[T comparable] struct{ forward, reverse map[T]T }

func (m *identities[T]) match(a, b T) bool {
	var zero T
	if a == zero || b == zero {
		return a == b
	}
	if v, ok := m.forward[a]; ok {
		return v == b
	}
	if _, ok := m.reverse[b]; ok {
		return false
	}
	if m.forward == nil {
		m.forward = make(map[T]T)
		m.reverse = make(map[T]T)
	}
	m.forward[a] = b
	m.reverse[b] = a
	return true
}

type correlations struct {
	token, attempt, channel, generation, request identities[uint64]
}

type aliases struct{ peer, port identities[string] }

func relative(value, origin core.Instant) (core.Instant, error) {
	if (origin > 0 && value < core.Instant(math.MinInt64)+origin) || (origin < 0 && value > core.Instant(math.MaxInt64)+origin) {
		return 0, &InvalidError{}
	}
	return value - origin, nil
}

func within(a, b core.Instant, tolerance time.Duration) bool {
	if a > b {
		a, b = b, a
	}
	// Unsigned subtraction after ordering handles the entire signed clock domain.
	return uint64(b)-uint64(a) <= uint64(tolerance)
}

// fields compares only the recorded allowlist. It emits names, never values:
// even a hand-edited recording containing secrets cannot put them in a report.
func fields(a, b tracerecord.Summary, leftOrigin, rightOrigin core.Instant, c *correlations, p *aliases, tolerance time.Duration) ([]string, error) {
	var result []string
	add := func(equal bool, name string) {
		if !equal {
			result = append(result, name)
		}
	}
	add(c.token.match(a.Token, b.Token), "token")
	add(c.attempt.match(a.Attempt, b.Attempt), "attempt")
	add(c.channel.match(uint64(a.Channel), uint64(b.Channel)), "channel")
	add(c.generation.match(a.Generation, b.Generation), "generation")
	add(c.request.match(a.Request, b.Request), "request")
	add(p.peer.match(a.Peer, b.Peer), "peer")
	add(p.port.match(a.PortAlias, b.PortAlias), "port_alias")
	if a.At == 0 || b.At == 0 {
		add(a.At == b.At, "at")
	} else {
		at, err := relative(a.At, leftOrigin)
		if err != nil {
			return nil, err
		}
		bt, err := relative(b.At, rightOrigin)
		if err != nil {
			return nil, err
		}
		add(within(at, bt, tolerance), "at")
	}
	// Summary contains only scalar allowlist members. Clear relational/time fields
	// already checked above, then enumerate the remaining fields in schema order.
	a.Token = 0
	b.Token = 0
	a.Attempt = 0
	b.Attempt = 0
	a.Channel = 0
	b.Channel = 0
	a.Generation = 0
	b.Generation = 0
	a.Request = 0
	b.Request = 0
	a.Peer = ""
	b.Peer = ""
	a.PortAlias = ""
	b.PortAlias = ""
	a.At = 0
	b.At = 0
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	for i := 0; i < av.NumField(); i++ {
		if av.Field(i).Interface() != bv.Field(i).Interface() {
			name := av.Type().Field(i).Tag.Get("json")
			for j, ch := range name {
				if ch == ',' {
					name = name[:j]
					break
				}
			}
			result = append(result, name)
		}
	}
	return result, nil
}

func knownMachine(s string) bool {
	switch s {
	case "iotc", "rdt", "tunnel", "session", "account", "allocation", "dtls", "lan":
		return true
	}
	return false
}

type stream struct {
	machine  string
	instance uint64
	steps    []tracerecord.Record
}

// Instances are paired by machine and first causal occurrence, independently of
// other machines' scheduling. We do not infer wire identities from erased data.
func streams(t trace) []stream {
	type key struct {
		machine  string
		instance uint64
	}
	index := make(map[key]int)
	var result []stream
	for _, r := range t.steps {
		k := key{r.Machine, r.Instance}
		i, ok := index[k]
		if !ok {
			i = len(result)
			index[k] = i
			result = append(result, stream{machine: r.Machine, instance: r.Instance})
		}
		result[i].steps = append(result[i].steps, r)
	}
	return result
}

func uncertain(s tracerecord.Summary) bool {
	return s.Unsupported || s.Peer == "peerOverflow" || s.PortAlias == "portOverflow"
}
