package tracerecord

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
)

type acknowledgedSink struct {
	memorySink
	written chan struct{}
}

func (s *acknowledgedSink) Write(b []byte) (int, error) {
	n, err := s.memorySink.Write(b)
	s.written <- struct{}{}
	return n, err
}

// Assert the public JSON independently of Record's decoder, including omission
// on step records. Existing readers can ignore the new end-only member.
func checkDropCauses(t *testing.T, b []byte, want map[string]uint64) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(b), []byte{'\n'})
	for i, line := range lines {
		var r struct {
			Record  string            `json:"record"`
			Dropped uint64            `json:"dropped"`
			Causes  map[string]uint64 `json:"drop_causes"`
		}
		if err := json.Unmarshal(line, &r, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		if i < len(lines)-1 {
			if r.Causes != nil {
				t.Fatal("step contains drop causes")
			}
			continue
		}
		var total uint64
		for cause, n := range want {
			if r.Causes[cause] != n {
				t.Errorf("%s losses=%d want %d", cause, r.Causes[cause], n)
			}
			total += n
		}
		for cause, n := range r.Causes {
			if n != want[cause] {
				t.Errorf("unexpected %s losses=%d", cause, n)
			}
		}
		if r.Record != "end" || r.Dropped != total {
			t.Errorf("trailer kind=%s dropped=%d want end/%d", r.Record, r.Dropped, total)
		}
		if total == 0 && r.Causes != nil {
			t.Error("lossless trailer contains drop causes")
		}
	}
}

// This is a recorder traffic-shape regression, not an SMB codec test. Synthetic
// entries become transport-sized chunks, followed by two 19 KB pulls and HTTP.
// The clock gate forces the overlap between owner and DTLS completion hooks
// without relying on scheduler speed or a slow disk. Acknowledgements are only
// a test seam: the production owner never waits for trace storage.
func TestLightSessionTraceContention(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		t.Run(map[bool]string{false: "serial", true: "overlapping hooks"}[overlap], func(t *testing.T) {
			s := &acknowledgedSink{written: make(chan struct{}, 1)}
			var entered, release chan struct{}
			var gate atomic.Bool
			w := New(s, SizeCap, func() time.Time {
				if entered != nil && gate.CompareAndSwap(true, false) {
					close(entered)
					<-release
				}
				return time.Unix(0, 0)
			})
			var now core.Instant
			record := func(machine string, input, output any) {
				w.Record(machine, 1, now, input, output)
				<-s.written
			}
			chunk := func(n int) {
				record("dtls", EdgeEvent{Kind: 6, Length: 1388}, []EdgeEvent{{Kind: 7, Length: n}})
				record("rdt", rdt.Event{Kind: rdt.Receive, Packet: rdt.Packet{Type: rdt.Data, Payload: make([]byte, n)}}, []rdt.Action{{Kind: rdt.ArmTimer, At: now + 1}})
				record("tunnel", p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: make([]byte, n)}, []p2ptunnel.Action{{Kind: p2ptunnel.WriteLocal, Data: make([]byte, n)}})
			}
			collisions := 0
			collide := func(count int) {
				if !overlap {
					return
				}
				for range count {
					entered, release = make(chan struct{}), make(chan struct{})
					gate.Store(true)
					done := make(chan struct{})
					// Alternate the producer that holds the recorder lock.
					go func() {
						if collisions%2 == 0 {
							w.Record("rdt", 1, now, rdt.Event{Kind: rdt.Receive}, []rdt.Action{})
						} else {
							w.RecordDTLS(2, 6, 1388, 1024, 1, "none")
						}
						close(done)
					}()
					<-entered
					competing := make(chan struct{})
					go func() {
						if collisions%2 == 0 {
							w.RecordDTLS(2, 6, 1388, 1024, 1, "none")
						} else {
							w.Record("rdt", 1, now, rdt.Event{Kind: rdt.Receive}, []rdt.Action{})
						}
						close(competing)
					}()
					// A bounded wait gives the competing hook an opportunity
					// to enter while the injected clock holds the first hook.
					// TryLock returns (and drops) immediately; Lock waits until
					// release. Both schedules must retain both records.
					select {
					case <-competing:
					case <-time.After(time.Millisecond):
					}
					close(release)
					<-done
					<-competing
					<-s.written
					if w.Dropped() != 0 {
						t.Fatalf("overlapping hooks dropped %d steps", w.Dropped())
					}
					<-s.written
					entered, release = nil, nil
					collisions++
				}
			}
			for range 15 {
				now += core.Instant(2 * time.Second)
				chunk(512)
			}
			collide(1)
			chunk(256) // share list
			collide(3)
			for entries := 340; entries > 0; entries -= 4 {
				chunk(4 * 256)
			}
			collide(41)
			for _, losses := range []int{20, 12} {
				for remaining := 19029; remaining > 0; remaining -= 1024 {
					chunk(min(remaining, 1024))
				}
				collide(losses)
			}
			chunk(160) // HTTP GET
			w.Close()
			want := uint64(0)
			if got := w.Dropped(); got != want {
				t.Fatalf("drops=%d want %d", got, want)
			}
			checkDropCauses(t, s.Bytes(), map[string]uint64{"contention": want})
			rr, err := Read(bytes.NewReader(s.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			for i, r := range rr {
				if r.Sequence != uint64(i+1) {
					t.Fatalf("sequence=%d want %d", r.Sequence, i+1)
				}
			}
		})
	}
}

func TestAdmissionDropCauses(t *testing.T) {
	s := &blockedSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	w := New(s, SizeCap, func() time.Time { return time.Unix(0, 0) })
	w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
	<-s.entered
	for range queueCapacity + 1 {
		w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
	}
	w.Record("unknown", 1, 0, rdt.Event{}, []rdt.Action{})
	w.Record("rdt", 1, 0, rdt.Event{}, 1)
	w.Record("rdt", 1, 0, rdt.Event{}, make([]rdt.Action, 2049))
	close(s.release)
	w.Close()
	checkDropCauses(t, s.Bytes(), map[string]uint64{"queue_full": 1, "invalid": 2, "transaction_limit": 1})
}

func TestCapDropCause(t *testing.T) {
	s := new(memorySink)
	w := New(s, trailerReserve, func() time.Time { return time.Unix(0, 0) })
	w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
	w.Close()
	checkDropCauses(t, s.Bytes(), map[string]uint64{"file_cap": 1})
}

func TestEncodedTransactionDropCause(t *testing.T) {
	s := new(memorySink)
	w := New(s, SizeCap, func() time.Time { return time.Unix(0, 0) })
	actions := make([]p2ptunnel.Action, 2048)
	for i := range actions {
		actions[i] = p2ptunnel.Action{Kind: p2ptunnel.WriteLocal, Channel: 255, Generation: math.MaxUint64, Token: math.MaxUint64, Request: math.MaxUint64, At: math.MaxInt64}
	}
	w.Record("tunnel", 1, 0, p2ptunnel.Event{}, actions)
	w.Close()
	checkDropCauses(t, s.Bytes(), map[string]uint64{"transaction_limit": 1})
}

func TestDropTrailerFitsReserve(t *testing.T) {
	causes := &DropCauses{math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64}
	b, err := json.Marshal(Record{Version: 1, Record: "end", Sequence: math.MaxUint64, UTC: "2000-01-01T00:00:00.999999999Z", Dropped: math.MaxUint64, DropCauses: causes})
	if err != nil || len(b)+1 > trailerReserve {
		t.Fatalf("trailer bytes=%d reserve=%d err=%v", len(b)+1, trailerReserve, err)
	}
}

func TestClosedDropCause(t *testing.T) {
	w := New(new(memorySink), SizeCap, func() time.Time { return time.Unix(0, 0) })
	w.Close()
	w.Record("rdt", 1, 0, rdt.Event{}, []rdt.Action{})
	if w.Dropped() != 1 || w.causes.closed.Load() != 1 {
		t.Fatal("post-close recording did not count a closed loss")
	}
}

// Four producers submit 5,000 steps in 38-step bursts. The injected owner
// timestamps place each burst inside 4 ms of a 43 s session. Write acknowledgements
// deliberately hold the sink until ALL producer calls in the burst have returned;
// this also proves the inline hooks do not wait for storage or queue space.
func TestConcurrentSessionBursts(t *testing.T) {
	s := &acknowledgedSink{written: make(chan struct{}, 1)}
	w := New(s, SizeCap, func() time.Time { return time.Unix(0, 0) })
	defer func() {
		// Allow even a failing schedule to drain the worker and its end record.
		drained := make(chan struct{})
		go func() {
			for {
				select {
				case <-s.written:
				case <-w.done:
					close(drained)
					return
				}
			}
		}()
		w.Close()
		<-drained
	}()
	const steps, burst, producers = 5000, 38, 4
	peakQueue := 0
	for base := 0; base < steps; base += burst {
		n := min(burst, steps-base)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for p := range producers {
			wg.Go(func() {
				<-start
				for i := p; i < n; i += producers {
					now := core.Instant(int64(base/burst)*int64(43*time.Second)/(steps/burst) + int64(i)*int64(4*time.Millisecond)/burst)
					if p%2 == 0 {
						w.Record("dtls", uint64(p+1), now, EdgeEvent{Kind: 16, Length: 1388}, []EdgeEvent{{Kind: 17, Length: 1024, Outcome: "none"}})
					} else {
						w.Record("rdt", uint64(p+1), now, rdt.Event{Kind: rdt.Receive, Packet: rdt.Packet{Type: rdt.Data, Payload: make([]byte, 1024)}}, []rdt.Action{{Kind: rdt.ArmTimer, At: now + 1}})
					}
				}
			})
		}
		close(start)
		joined := make(chan struct{})
		go func() { wg.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Fatal("producers waited on blocked storage")
		}
		peakQueue = max(peakQueue, len(w.queue))
		if w.Dropped() != 0 {
			t.Fatalf("burst at %d: contention=%d queue_full=%d dropped=%d", base, w.causes.contention.Load(), w.causes.queueFull.Load(), w.Dropped())
		}
		for range n {
			<-s.written
		}
	}
	w.Close()
	if w.causes.contention.Load() != 0 || w.causes.queueFull.Load() != 0 || w.Dropped() != 0 {
		t.Fatalf("drops=%d, causes=%+v", w.Dropped(), w.causes.snapshot())
	}
	t.Logf("steps=%d burst=%d producers=%d peak queue=%d capacity=%d encoded bytes=%d", steps, burst, producers, peakQueue, cap(w.queue), s.Len())
	checkDropCauses(t, s.Bytes(), nil)
	rr, err := Read(bytes.NewReader(s.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rr) != steps+1 {
		t.Fatalf("records=%d want %d", len(rr), steps+1)
	}
	for i, r := range rr {
		if r.Sequence != uint64(i+1) {
			t.Fatalf("sequence=%d want %d", r.Sequence, i+1)
		}
	}
}
