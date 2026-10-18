// Package tracetest captures simulator steps through the production recorder.
// Storage acknowledgement belongs only to tests, never to a protocol driver.
package tracetest

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"os"
	"testing"
	"time"
)

type sink struct {
	bytes.Buffer
	written chan struct{}
}

func (s *sink) Write(b []byte) (int, error) {
	n, err := s.Buffer.Write(b)
	s.written <- struct{}{}
	return n, err
}
func (*sink) Close() error { return nil }

// Recorder owns a production writer with a deterministic UTC clock.
type Recorder struct {
	t      testing.TB
	writer *tracerecord.Writer
	sink   *sink
}

// New constructs a capture with bounded acknowledged storage.
func New(t testing.TB, servicePorts ...uint16) *Recorder {
	s := &sink{written: make(chan struct{}, 1)}
	w := tracerecord.New(s, tracerecord.SizeCap, func() time.Time { return time.Unix(0, 0) })
	w.SetServicePorts(servicePorts)
	return &Recorder{t: t, sink: s, writer: w}
}

// Step waits for storage only in the simulator test thread.
func (r *Recorder) Step(machine string, id uint64, e any, actions any, now core.Instant) {
	r.t.Helper()
	r.writer.Record(machine, id, now, e, actions)
	select {
	case <-r.sink.written:
	case <-time.After(5 * time.Second):
		r.t.Fatal("simulator capture did not reach storage")
	}
}

// Fixture compares a complete recording with a checked-in redacted fixture.
// TRACE_UPDATE=1 explicitly regenerates fixtures; normal runs never write them.
func (r *Recorder) Fixture(path string) []tracerecord.Record {
	r.t.Helper()
	r.writer.Close()
	if r.writer.Dropped() != 0 {
		r.t.Fatalf("simulator capture dropped %d steps", r.writer.Dropped())
	}
	if os.Getenv("TRACE_UPDATE") == "1" {
		if err := os.WriteFile(path, r.sink.Bytes(), 0644); err != nil {
			r.t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatal(err)
	}
	if !bytes.Equal(r.sink.Bytes(), want) {
		r.t.Fatalf("recorded trace differs from %s", path)
	}
	records, err := tracerecord.Read(bytes.NewReader(want))
	if err != nil {
		r.t.Fatal(err)
	}
	return records
}
