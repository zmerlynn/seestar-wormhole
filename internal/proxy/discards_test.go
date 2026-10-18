package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/edge"
)

func TestDTLSDiscardSummary(t *testing.T) {
	// Literal RFC 6347 application and CCS records; both have epoch one.
	app := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42}
	control := []byte{20, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1}
	for _, tc := range []struct {
		rule    string
		index   int
		trigger func(*edge.DTLSPackets) error
		want    error
	}{
		{"R027", 0, func(p *edge.DTLSPackets) error { return p.Deliver(1, app) }, nil},
		{"R027", 0, func(p *edge.DTLSPackets) error {
			mixed := append(append([]byte(nil), app...), control...)
			if err := p.Deliver(1, mixed); err != nil {
				return err
			}
			b := make([]byte, 1388)
			n, _, err := p.ReadFrom(b)
			if err == nil && !bytes.Equal(b[:n], control) {
				return errors.New("mixed control record changed")
			}
			return err
		}, nil},
		{"R028", 1, func(p *edge.DTLSPackets) error {
			for range 32 {
				if err := p.Deliver(1, control); err != nil {
					return err
				}
			}
			return p.Deliver(1, control)
		}, edge.ErrDTLSQueueFull},
		{"R131", 2, func(p *edge.DTLSPackets) error {
			if err := p.Deliver(1, control); err != nil {
				return err
			}
			_, _, err := p.ReadFrom(make([]byte, 1))
			return err
		}, io.ErrShortBuffer},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			var logs bytes.Buffer
			p, _ := edge.NewDTLSPackets(1388)
			defer p.Close()
			x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))}}, packets: p}
			x.initDiscardSummary(0)
			if err := tc.trigger(p); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if got := p.Discards()[tc.index]; got != 1 {
				t.Fatalf("counter = %d, want 1", got)
			}
			x.discardSummary(core.Instant(discardInterval) - 1)
			if logs.Len() != 0 {
				t.Fatalf("early log: %s", &logs)
			}
			x.discardSummary(core.Instant(discardInterval))
			x.discardSummary(core.Instant(discardInterval))
			x.discardSummary(core.Instant(2 * discardInterval))
			if strings.Count(logs.String(), `"level":"WARN"`) != 1 || !strings.Contains(logs.String(), `"rule":"`+tc.rule+`"`) || !strings.Contains(logs.String(), `"count":1`) {
				t.Fatalf("summary = %s", &logs)
			}
		})
	}
}

func TestIngressDiscardSummary(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    layerEvent
	}{
		{"iotc", layerEvent{i: &iotc.Event{Kind: iotc.Wire}}},
		{"rdt", layerEvent{r: &rdt.Event{Kind: rdt.Receive}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&logs, nil))}}, queue: make([]layerEvent, 256)}
			x.initDiscardSummary(0)
			for range 100 {
				x.enqueue(tc.e)
			}
			if len(x.queue) != 256 || x.ingressDiscards != 100 {
				t.Fatalf("queue=%d discards=%d", len(x.queue), x.ingressDiscards)
			}
			x.discardSummary(core.Instant(discardInterval) - 1)
			if logs.Len() != 0 {
				t.Fatalf("early log: %s", &logs)
			}
			x.discardSummary(core.Instant(discardInterval))
			x.discardSummary(core.Instant(discardInterval))
			x.discardSummary(core.Instant(2 * discardInterval))
			if strings.Count(logs.String(), `"level":"WARN"`) != 1 || !strings.Contains(logs.String(), `"rule":"R072"`) || !strings.Contains(logs.String(), `"count":100`) {
				t.Fatalf("summary = %s", &logs)
			}
		})
	}
}

func TestProtocolDiscardSummaries(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		run        func(*stack)
	}{
		{"iotc", "R039", func(x *stack) {
			x.iotc, _ = iotc.New(iotc.Config{Attempt: 1, UID: [20]byte{1}, License: iotc.License{Realm: "x"}})
			for range 100 {
				x.iotc.Step(iotc.Event{Attempt: 2, Kind: iotc.Wire}, 0)
			}
		}},
		{"rdt", "R087", func(x *stack) {
			x.reliable, _ = rdt.New(rdt.DefaultConfig())
			for range 100 {
				x.reliable.Step(rdt.Event{Kind: rdt.Receive}, 0)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))}}}
			x.initDiscardSummary(0)
			tc.run(x)
			x.discardSummary(core.Instant(discardInterval) - 1)
			if logs.Len() != 0 {
				t.Fatalf("early log: %s", &logs)
			}
			x.discardSummary(core.Instant(discardInterval))
			x.discardSummary(core.Instant(discardInterval))
			x.discardSummary(core.Instant(2 * discardInterval))
			if strings.Count(logs.String(), `"level":"WARN"`) != 1 || !strings.Contains(logs.String(), `"rule":"`+tc.rule+`"`) || !strings.Contains(logs.String(), `"count":100`) {
				t.Fatalf("summary = %s", &logs)
			}
		})
	}
}

func TestSessionDiscardSummary(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelWarn, slog.LevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			var logs bytes.Buffer
			s := &Session{logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level}))}
			m, _ := session.New(session.DefaultConfig(), rand.NewPCG(1, 2))
			for range 100 {
				m.Step(session.Event{Kind: session.Progress, Attempt: 999}, 0)
			}
			s.Observe(session.Event{}, session.Snapshot{}, m.Snapshot(), 0)
			if s.ObservationDeadline() != core.Instant(discardInterval) {
				t.Fatalf("deadline=%d", s.ObservationDeadline())
			}
			if logs.Len() != 0 {
				t.Fatalf("early log: %s", &logs)
			}
			for _, now := range []core.Instant{core.Instant(discardInterval), core.Instant(discardInterval), core.Instant(2 * discardInterval)} {
				s.Observe(session.Event{}, m.Snapshot(), m.Snapshot(), now)
			}
			if strings.Count(logs.String(), `"level":"WARN"`) != 1 || !strings.Contains(logs.String(), `"rule":"R097"`) || !strings.Contains(logs.String(), `"count":100`) {
				t.Fatalf("summary = %s", &logs)
			}
		})
	}
}

func TestDiscardOnlyWakeup(t *testing.T) {
	var logs bytes.Buffer
	x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&logs, nil))}}, timerUpdates: make(chan time.Duration, 1)}
	x.initDiscardSummary(0)
	x.ingressDiscards = 1
	if !x.telemetryOnlyTick(core.Instant(discardInterval)) {
		t.Fatal("discard-only wakeup injected protocol ticks")
	}
	if !strings.Contains(logs.String(), `"rule":"R072"`) {
		t.Fatalf("summary = %s", &logs)
	}
	if x.discardNext != core.Instant(2*discardInterval) {
		t.Fatalf("next=%d", x.discardNext)
	}
}

func TestDiscardReleaseSummary(t *testing.T) {
	for _, state := range []session.State{session.Dormant, session.Closed} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			var logs bytes.Buffer
			s := &Session{logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))}
			x := &stack{f: &factory{s: s}, ingressDiscards: 5}
			x.initDiscardSummary(0)
			s.discardStack.Store(x)
			for range 2 {
				s.Observe(session.Event{Kind: session.Released, Attempt: 1}, session.Snapshot{State: session.Releasing, Attempt: 1}, session.Snapshot{State: state, Attempt: 1, Discards: 3}, 1)
			}
			if strings.Count(logs.String(), `"rule":"R072"`) != 1 || !strings.Contains(logs.String(), `"count":5`) || strings.Count(logs.String(), `"rule":"R097"`) != 1 || !strings.Contains(logs.String(), `"count":3`) {
				t.Fatalf("final summaries = %s", &logs)
			}
			if s.discardStack.Load() != nil || s.ObservationDeadline() != 0 {
				t.Fatal("released attempt retained reporting state")
			}
		})
	}
}

func TestStaleReleaseKeepsDiscardSummary(t *testing.T) {
	var logs bytes.Buffer
	s := &Session{logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))}
	x := &stack{f: &factory{s: s}, ingressDiscards: 5}
	s.discardStack.Store(x)
	s.Observe(session.Event{Kind: session.Released, Attempt: 99}, session.Snapshot{State: session.Ready, Attempt: 1}, session.Snapshot{State: session.Ready, Attempt: 1, Discards: 1}, 0)
	if s.discardStack.Load() != x || logs.Len() != 0 {
		t.Fatalf("stale release drained active counters: %s", &logs)
	}
	// The final joined-cleanup observer passes the same snapshot twice.
	final := session.Snapshot{State: session.Ready, Attempt: 1, Discards: 1}
	s.Observe(session.Event{Kind: session.Released}, final, final, 1)
	if s.discardStack.Load() != nil || !strings.Contains(logs.String(), `"rule":"R072"`) {
		t.Fatalf("final cleanup did not report loss: %s", &logs)
	}
}

func TestDriveArmsDiscardSummary(t *testing.T) {
	var logs bytes.Buffer
	x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&logs, nil))}}, ctx: context.Background(), timerUpdates: make(chan time.Duration, 1)}
	x.drive(layerEvent{}, 0)
	if x.discardNext != core.Instant(discardInterval) {
		t.Fatalf("discard deadline=%d, want %d", x.discardNext, discardInterval)
	}
	select {
	case delay := <-x.timerUpdates:
		if delay != discardInterval {
			t.Fatalf("timer delay=%v, want %v", delay, discardInterval)
		}
	default:
		t.Fatal("drive did not arm the owner timer")
	}
	x.ingressDiscards = 3
	if x.telemetryOnlyTick(core.Instant(discardInterval)-1) || logs.Len() != 0 {
		t.Fatal("early summary")
	}
	if !x.telemetryOnlyTick(core.Instant(discardInterval)) {
		t.Fatal("discard interval not consumed")
	}
	if strings.Count(logs.String(), `"level":"WARN"`) != 1 || !strings.Contains(logs.String(), `"rule":"R072","count":3`) {
		t.Fatalf("missing interval WARN before release: %s", &logs)
	}
}
