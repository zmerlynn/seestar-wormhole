package iotc

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// This oracle observes requests and actual Send effects, not endpoint stages,
// candidate tables or selector fields. The short ownership property covers
// arbitrary response permutations; this one drives ArmTimer through the full
// attempt lifetime and gives every retained branch a bounded progress obligation.
func TestPathProgressSeeds(t *testing.T) {
	for seed := uint64(1); seed <= 128; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runProgressSeed(t, seed); runDiscoveryProgressSeed(t, seed) })
	}
}

type progressEvent struct {
	event  Event
	plain  []byte // Wire: edge authentication result to return later
	reject bool
	opcode uint16 // successful authentication's model transition
}
type progressOwner struct {
	live, checked, accepted       bool
	joined, directSent, relaySent core.Instant
	acceptedAt                    core.Instant
}
type progressRig struct {
	t        *testing.T
	m        *Machine
	rng      *rand.Rand
	schedule *sim.Scheduler[progressEvent]
	timer    *sim.Timer[progressEvent]
	owners   map[Link]*progressOwner
	selected Transport
	sent     map[string]Action
	trace    []string
	terminal bool
}

func newProgressRig(t *testing.T, seed uint64, transport Transport) *progressRig {
	m, _ := New(config())
	m.started, m.state, m.material, m.hasMaterial = true, Connecting, material(), true
	q := sim.New[progressEvent](1<<20, 2048)
	return &progressRig{t: t, m: m, rng: rand.New(rand.NewPCG(seed, 303)), schedule: q, timer: sim.NewTimer(q), owners: map[Link]*progressOwner{}, selected: transport, sent: map[string]Action{}}
}
func (r *progressRig) queue(at core.Instant, e progressEvent) {
	r.t.Helper()
	if err := r.schedule.Schedule(at, e, len(e.event.Data)+len(e.plain)); err != nil {
		r.t.Fatal(err)
	}
}
func (r *progressRig) server(n int, transport Transport) Link {
	l := Link{Transport: transport, Address: netip.AddrPortFrom(netip.AddrFrom4([4]byte{203, 0, 113, byte(n + 1)}), 10001)}
	if transport == TCP {
		l.ID = uint64(n + 100)
	}
	idx, _ := r.m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
	r.m.cache.Entries[idx].Key, r.m.cache.Entries[idx].CachedMarker = 3, Marker{10, 1}
	r.m.add(endpoint{link: l, connected: true, stage: precheckPending, cache: idx})
	r.owners[l] = &progressOwner{live: true}
	return l
}
func (r *progressRig) record(at core.Instant, l Link, op uint16, reject bool) {
	var p Packet
	cfg, mat := config(), material()
	typ := byte(4)
	switch op {
	case 0x0218:
		typ = 3
		p = controlPacket(op, 0x24, 56)
		put16(p.Body, material().P2PNonce)
		put32(p.Body[4:], 0x55)
		copy(p.Body[8:], cfg.UID[:])
	case 0x020d:
		p = controlPacket(op, 0x24, 32)
		copy(p.Body, cfg.UID[:])
		copy(p.Body[24:], mat.RID[:])
	}
	raw, _ := p.Marshal()
	prefix := 0
	if typ == 4 {
		prefix = 4
	}
	header, _ := RecordHeader(typ, config().RecordSID, prefix+len(raw)+16)
	if typ == 4 {
		header = append(header, 0x55, 0, 0, 0)
	}
	r.queue(at, progressEvent{event: Event{Kind: Wire, Link: l, Data: append(header, make([]byte, len(raw)+16)...)}, plain: raw, opcode: op, reject: reject})
}
func (r *progressRig) consume(out []Action, input progressEvent) {
	now := r.schedule.Now()
	for _, a := range out {
		switch a.Kind {
		case ArmTimer:
			if err := r.timer.Arm(a.At, progressEvent{event: Event{Kind: Tick}}); err != nil {
				r.t.Fatal(err)
			}
		case CancelTimer:
			r.timer.Cancel()
		case OpenTCP:
			// A failed port creates a new generation. Old queued Wire, Connected and
			// crypto completions still arrive, but cannot authorize work on the new link.
			r.owners[a.Link] = &progressOwner{live: true}
			r.queue(now.Add(time.Duration(1+r.rng.IntN(60))*time.Millisecond), progressEvent{event: Event{Kind: Connected, Link: a.Link}})
			r.record(now.Add(2600*time.Millisecond), a.Link, 0x0218, false)
		case Seal:
			// Finish each setup and retry separately, in randomized order. The edge
			// contract is mocked here; the retained network regressions use real GCM.
			cipher := make([]byte, len(a.Data)+16)
			for i := 0; i < 8; i++ {
				cipher[i] = byte(a.Token >> uint(i*8))
			}
			r.sent[string(append(append([]byte(nil), a.AAD...), cipher...))] = a
			delay := time.Duration(1+r.rng.IntN(60)) * time.Millisecond
			r.queue(now.Add(delay), progressEvent{event: Event{Kind: Encrypted, Token: a.Token, Link: a.Link, Data: cipher}})
		case Open:
			kind := Decrypted
			if input.reject {
				kind = CryptoRejected
			}
			r.queue(now.Add(time.Duration(1+r.rng.IntN(60))*time.Millisecond), progressEvent{event: Event{Kind: kind, Token: a.Token, Link: a.Link, Data: input.plain}, opcode: input.opcode})
		case Send:
			if seal, ok := r.sent[string(a.Data)]; ok {
				op := le16(seal.Data[8:])
				v := r.owners[a.Link]
				if !v.live {
					r.t.Fatalf("retired generation emitted %04x on %v", op, a.Link)
				}
				if op == 0x0240 {
					v.directSent = now
				}
				if (!v.accepted && op == 0x020c) || (v.accepted && op == 0x0241) {
					v.relaySent = now
				}
				r.trace = append(r.trace, fmt.Sprintf("%d:%v:%04x", now, a.Link, op))
			}
		case Failed:
			if now < core.Instant(60*time.Second) {
				r.t.Fatalf("premature failure at %v: %s", now, a.Reason)
			}
			r.terminal = true
		}
	}
}
func (r *progressRig) step(input progressEvent) {
	now := r.schedule.Now()
	e := input.event
	e.Attempt = 1
	if v := r.owners[e.Link]; v != nil && v.live {
		if e.Kind == LinkFailed {
			v.live = false
		}
		if e.Kind == Decrypted && e.Link.Transport == r.selected {
			if input.opcode == 0x0218 && !v.checked {
				v.checked = true
				v.joined = now
			}
			if input.opcode == 0x020d && v.checked && !v.accepted {
				v.accepted = true
				v.acceptedAt = now
				v.relaySent = 0
			}
		}
	}
	r.consume(r.m.Step(e, now), input)
	r.check()
}
func (r *progressRig) check() {
	r.t.Helper()
	now := r.schedule.Now()
	if now >= core.Instant(60*time.Second) || r.terminal {
		return
	}
	for l, v := range r.owners {
		if !v.live || !v.checked || l.Transport != r.selected {
			continue
		}
		joined := v.joined
		if l.Transport == TCP {
			joined = max(joined, core.Instant(1500*time.Millisecond))
		}
		// 60 ms bounded edge latency + owner poll; allow 100 ms, independently of
		// production retry bookkeeping. A response on the other branch grants no delay.
		if l.Transport == UDP && now.Sub(max(joined, v.directSent)) > 600*time.Millisecond {
			r.t.Fatalf("owned direct negotiation stopped progressing on %v at %v: last Send=%v trace=%v", l, now, v.directSent, r.trace)
		}
		interval := 600 * time.Millisecond
		if l.Transport == TCP {
			interval = 2100 * time.Millisecond
		}
		if now.Sub(max(joined, v.acceptedAt, v.relaySent)) > interval {
			r.t.Fatalf("prechecked server stranded on %v at %v: accepted=%v last appropriate relay Send=%v trace=%v", l, now, v.accepted, v.relaySent, r.trace)
		}
	}
}
func runProgressSeed(t *testing.T, seed uint64) {
	transport := UDP
	if seed%2 == 0 {
		transport = TCP
	}
	r := newProgressRig(t, seed, transport)
	links := []Link{r.server(0, transport), r.server(1, transport), r.server(2, transport)}
	other := TCP
	if transport == TCP {
		other = UDP
	}
	retired := r.server(3, other)
	// Seeds 1 and 2 cover a lost direct flight with an accepted relay, and one
	// endpoint failing before another endpoint's late precheck. Other seeds jitter both failure orders,
	// precheck completion, rejection, and individual crypto completion ordering.
	r.record(core.Instant(100*time.Millisecond), links[0], 0x0218, false)
	late := 2200 * time.Millisecond
	if seed > 2 {
		late = time.Duration(1700+r.rng.IntN(4000)) * time.Millisecond
	}
	r.record(core.Instant(late), links[1], 0x0218, false)
	r.record(core.Instant(late+300*time.Millisecond), links[2], 0x0218, false)
	// Model successful acceptance on a retained endpoint on both transports;
	// subsequent progress requires 0241, not another 020c for the completed phase.
	r.record(core.Instant(late+time.Duration(250+r.rng.IntN(2000))*time.Millisecond), links[1], 0x020d, false)
	failAt := late - 100*time.Millisecond
	if seed > 2 && r.rng.IntN(2) == 0 {
		failAt = late + 200*time.Millisecond
	}
	if transport == TCP {
		r.queue(core.Instant(failAt), progressEvent{event: Event{Kind: LinkFailed, Link: links[0]}})
	} else {
		r.record(core.Instant(450*time.Millisecond), links[0], 0x020d, false)
	}
	// Rejected ingress owns a real Open until its individual completion. A
	// repeated valid precheck follows, without a manually reserved operation.
	r.record(core.Instant(700*time.Millisecond), links[1], 0x0218, true)
	otherAt := 1700 * time.Millisecond
	if transport == UDP {
		otherAt = 50 * time.Millisecond
	}
	r.record(core.Instant(otherAt), retired, 0x0218, false)
	if transport == TCP && seed > 2 && seed%4 == 0 {
		r.queue(core.Instant(200*time.Millisecond), progressEvent{event: Event{Kind: LinkFailed, Link: links[2]}})
		r.queue(core.Instant(300*time.Millisecond), progressEvent{event: Event{Kind: Connected, Link: links[2]}})
	}
	r.queue(0, progressEvent{event: Event{Kind: Tick}})
	if err := r.schedule.Run(core.Instant(60*time.Second), 20000, func(e progressEvent, _ core.Instant) error { r.step(e); return nil }); err != nil {
		t.Fatal(err)
	}
	if !r.terminal || r.m.State() != Closed {
		t.Fatalf("deadline state=%v failed=%v, want Closed and Failed", r.m.State(), r.terminal)
	}
}
