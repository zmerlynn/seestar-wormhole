package sim

import (
	"container/heap"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"math/rand/v2"
	"time"
)

var ErrBudget = errors.New("simulator queue budget exceeded")
var ErrTime = errors.New("simulator cannot schedule in the past")

// Item has a stable insertion order for equal deadlines. Bytes is accounting
// supplied by the caller, including any payload reachable from Event.
type Item[E any] struct {
	At    core.Instant
	Event E
	Bytes int
	order uint64
}
type queue[E any] []Item[E]

func (q queue[E]) Len() int { return len(q) }
func (q queue[E]) Less(i, j int) bool {
	if q[i].At == q[j].At {
		return q[i].order < q[j].order
	}
	return q[i].At < q[j].At
}
func (q queue[E]) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue[E]) Push(v any)   { *q = append(*q, v.(Item[E])) }
func (q *queue[E]) Pop() any {
	a := *q
	i := len(a) - 1
	v := a[i]
	var zero Item[E]
	a[i] = zero
	*q = a[:i]
	return v
}

// Scheduler advances only virtual monotonic time. Wall jumps cannot prematurely
// expire protocol deadlines. It is single-owner, including handler invocation.
type Scheduler[E any] struct {
	now                  core.Instant
	wall                 time.Duration
	serial               uint64
	q                    queue[E]
	budget               core.Budget
	PeakBytes, PeakCount int
}

func New[E any](maxBytes, maxEvents int) *Scheduler[E] {
	return &Scheduler[E]{budget: core.Budget{MaxBytes: maxBytes, MaxCount: maxEvents}}
}
func (s *Scheduler[E]) Now() core.Instant            { return s.now }
func (s *Scheduler[E]) Wall() time.Duration          { return time.Duration(s.now) + s.wall }
func (s *Scheduler[E]) JumpWall(delta time.Duration) { s.wall += delta }
func (s *Scheduler[E]) Usage() (int, int)            { return s.budget.Bytes, s.budget.Count }
func (s *Scheduler[E]) Schedule(at core.Instant, event E, bytes int) error {
	if at < s.now {
		return ErrTime
	}
	if !s.budget.Reserve(bytes, 1) {
		return ErrBudget
	}
	s.serial++
	heap.Push(&s.q, Item[E]{at, event, bytes, s.serial})
	s.PeakBytes = max(s.PeakBytes, s.budget.Bytes)
	s.PeakCount = max(s.PeakCount, s.budget.Count)
	return nil
}

// Run advances to until, dispatching at most limit events to prevent zero-time
// loops. An error preserves remaining events and the last dispatched time.
func (s *Scheduler[E]) Run(until core.Instant, limit int, handle func(E, core.Instant) error) error {
	if until < s.now {
		return ErrTime
	}
	for len(s.q) > 0 && s.q[0].At <= until {
		if limit <= 0 {
			return errors.New("simulator step limit exceeded")
		}
		limit--
		v := heap.Pop(&s.q).(Item[E])
		s.budget.Release(v.Bytes, 1)
		s.now = v.At
		if err := handle(v.Event, s.now); err != nil {
			return err
		}
	}
	s.now = until
	return nil
}

type Packet struct {
	From, To string
	Data     []byte
}

// Link is a datagram model: parts per million loss/duplication, uniform inclusive
// jitter, and explicit MTU. Different jitter causes reordering. ConsumerDelay
// spaces deliveries to a destination, representing a slow consumer. A reliable
// ordered stream must be supplied by RDT, never by applying this to tunnel bytes.
type Link struct {
	MTU                          int
	LossPPM, DuplicatePPM        int
	Delay, Jitter, ConsumerDelay time.Duration
}
type Network struct {
	Scheduler                 *NetworkScheduler
	rng                       *rand.Rand
	destinations              map[string]*destination
	MaxEndpoints              int
	Sent, Dropped, Duplicated int
}

// A destination queue is represented by service events in the shared bounded
// heap. Arrivals reserve service slots only when they arrive, in arrival order.
// Each packet occupies exactly one heap slot, in transit or awaiting service.
type destination struct {
	ready, horizon core.Instant
	pending        int
	queued         int
}
type networkEvent struct {
	packet  Packet
	delay   time.Duration
	service bool
}

// NetworkScheduler drives both arrivals and consumption. Run's limit counts
// internal arrival/service events as well as immediate deliveries.
type NetworkScheduler struct {
	*Scheduler[networkEvent]
	network *Network
}

func (s *NetworkScheduler) Run(until core.Instant, limit int, handle func(Packet, core.Instant) error) error {
	return s.Scheduler.Run(until, limit, func(e networkEvent, now core.Instant) error {
		d := s.network.destinations[e.packet.To]
		if !e.service {
			at := max(now, d.ready)
			d.ready = at.Add(e.delay) // Send preflights the destination's worst-case horizon.
			if at > now || d.queued > 0 {
				e.service = true
				d.queued++
				return s.Schedule(at, e, len(e.packet.Data))
			}
		}
		if e.service {
			d.queued--
		}
		d.pending--
		if d.pending == 0 {
			d.horizon = d.ready
		}
		return handle(e.packet, now)
	})
}

func NewNetwork(seed uint64, maxBytes, maxPackets int) *Network {
	n := &Network{rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), destinations: make(map[string]*destination), MaxEndpoints: 256}
	n.Scheduler = &NetworkScheduler{Scheduler: New[networkEvent](maxBytes, maxPackets), network: n}
	return n
}
func (n *Network) Send(p Packet, l Link) error {
	if l.MTU < 1 || l.LossPPM < 0 || l.LossPPM > 1000000 || l.DuplicatePPM < 0 || l.DuplicatePPM > 1000000 || l.Delay < 0 || l.Jitter < 0 || l.Jitter == time.Duration(1<<63-1) || l.ConsumerDelay < 0 || l.Delay > time.Duration(1<<63-1)-l.Jitter {
		return errors.New("invalid link")
	}
	n.Sent++
	if len(p.Data) > l.MTU || n.rng.IntN(1000000) < l.LossPPM {
		n.Dropped++
		return nil
	}
	if _, ok := n.destinations[p.To]; !ok && len(n.destinations) >= n.MaxEndpoints {
		return ErrBudget
	}
	copies := 1
	if n.rng.IntN(1000000) < l.DuplicatePPM {
		copies++
	}
	// Reserve the whole send before copying, so budget rejection is atomic.
	b := n.Scheduler.budget
	if len(p.Data) > (b.MaxBytes-b.Bytes)/copies || copies > b.MaxCount-b.Count {
		return ErrBudget
	}
	var deadlines [2]core.Instant
	dest := n.destinations[p.To]
	var horizon core.Instant
	if dest != nil {
		horizon = dest.horizon
	}
	for i := 0; i < copies; i++ {
		d := l.Delay
		if l.Jitter > 0 {
			d += time.Duration(n.rng.Int64N(int64(l.Jitter) + 1))
		}
		if core.Instant(d) > core.Instant(1<<63-1)-n.Scheduler.Now() {
			return ErrTime
		}
		at := n.Scheduler.Now().Add(d)
		// Conservative overflow bound only: this never schedules consumption
		// or reserves a service slot ahead of actual network arrival.
		horizon = max(horizon, at)
		if core.Instant(l.ConsumerDelay) > core.Instant(1<<63-1)-horizon {
			return ErrTime
		}
		horizon = horizon.Add(l.ConsumerDelay)
		deadlines[i] = at
	}
	if dest == nil {
		dest = &destination{}
		n.destinations[p.To] = dest
	}
	dest.horizon = horizon
	dest.pending += copies

	for i := 0; i < copies; i++ {
		// Budget and all deadlines were checked before mutating the scheduler.
		if err := n.Scheduler.Schedule(deadlines[i], networkEvent{packet: Packet{p.From, p.To, append([]byte(nil), p.Data...)}, delay: l.ConsumerDelay}, len(p.Data)); err != nil {
			return err
		}
	}
	n.Duplicated += copies - 1
	return nil
}
