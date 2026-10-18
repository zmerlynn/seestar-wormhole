// Package tracerecord records redacted machine decisions outside protocol cores.
// A step is one JSON Lines transaction: an input and its ordered output actions.
package tracerecord

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"io"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// MaxLine bounds both queued transactions and replay decoder input.
const MaxLine = 256 << 10

// SizeCap bounds production trace files, including the final loss record.
const SizeCap = 16 << 20
const trailerReserve = 1024

// queueCapacity accommodates the measured 38-step directory-list burst even
// while storage is stalled, with room for concurrent completion hooks.
const queueCapacity = 64

// DropCauses counts losses by the recorder boundary that rejected them. It is
// emitted only on a lossy end record. Storage includes failed/short writes,
// discarded steps after sink failure, and failed closes. The trailer cannot
// report its own write failure or the subsequent sink close failure.
type DropCauses struct {
	Contention       uint64 `json:"contention,omitzero"`
	Closed           uint64 `json:"closed,omitzero"`
	Invalid          uint64 `json:"invalid,omitzero"`
	TransactionLimit uint64 `json:"transaction_limit,omitzero"`
	QueueFull        uint64 `json:"queue_full,omitzero"`
	FileCap          uint64 `json:"file_cap,omitzero"`
	Storage          uint64 `json:"storage,omitzero"`
}

type dropCounters struct {
	contention, closed, invalid, transactionLimit, queueFull, fileCap, storage atomic.Uint64
}

func (c *dropCounters) snapshot() *DropCauses {
	r := DropCauses{c.contention.Load(), c.closed.Load(), c.invalid.Load(), c.transactionLimit.Load(), c.queueFull.Load(), c.fileCap.Load(), c.storage.Load()}
	if r == (DropCauses{}) {
		return nil
	}
	return &r
}

// Record is the versioned comparison unit. Sequence is trace-wide; outputs are
// ordered by array index. MonoNS is the injected machine clock, UTC is edge time.
// Instances are local counters, never device or session wire identifiers.
type Record struct {
	Version    int          `json:"version"`
	Record     string       `json:"record"`
	Sequence   uint64       `json:"sequence"`
	Machine    string       `json:"machine,omitempty"`
	Instance   uint64       `json:"instance,omitzero"`
	MonoNS     core.Instant `json:"mono_ns"`
	UTC        string       `json:"utc"`
	Input      Summary      `json:"input,omitempty"`
	Outputs    []Summary    `json:"outputs,omitempty"`
	Dropped    uint64       `json:"dropped"`
	DropCauses *DropCauses  `json:"drop_causes,omitzero"`
}

// Writer owns a bounded queue and one storage worker. mu protects closure,
// sequence allocation, alias assignment and bounded projection/encoding.
// Producers serialize with Lock; queue insertion never waits for space, and
// the storage worker performs all I/O outside mu. The nil writer does no work
// and samples no clock.
type Writer struct {
	origin       time.Time
	mu           sync.Mutex
	closed       bool
	queue        chan []byte
	done         chan struct{}
	dropped      atomic.Uint64
	causes       dropCounters
	instances    atomic.Uint64
	sequence     uint64
	peers        map[string]string
	ports        map[uint16]string
	servicePorts map[uint16]bool
	now          func() time.Time
	logger       atomic.Pointer[slog.Logger]
	failed       chan struct{}
	reported     chan struct{}
	stop         chan struct{}
	stopOnce     sync.Once
	pending      atomic.Uint64
	timedOut     atomic.Bool
	nextWarn     atomic.Int64
	lastCount    atomic.Uint64
}

// Open creates a new 0600 file and refuses existing paths, including symlinks.
func Open(path string) (*Writer, error) {
	f, err := privatefile.Open(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return nil, errors.New("trace: cannot create private file")
	}
	return New(f, SizeCap, time.Now), nil
}

// New takes ownership of a sink. now is an injected UTC clock for simulators.
// capBytes must leave room for a trailer. Invalid caps discard all records.
func New(sink io.WriteCloser, capBytes int, now func() time.Time) *Writer {
	w := &Writer{origin: time.Now(), queue: make(chan []byte, queueCapacity), done: make(chan struct{}), peers: make(map[string]string), ports: make(map[uint16]string), servicePorts: make(map[uint16]bool), now: now, failed: make(chan struct{}, 1), reported: make(chan struct{}), stop: make(chan struct{})}
	go func() {
		defer close(w.reported)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-w.failed:
				w.report(false)
			case <-ticker.C:
				w.report(false)
			case <-w.done:
				w.report(true)
				return
			case <-w.stop:
				w.report(true)
				return
			}
		}
	}()
	go func() {
		defer close(w.done)
		defer func() {
			if err := sink.Close(); err != nil {
				w.drop(&w.causes.storage)
			}
		}()
		written := 0
		sinkFailed := false
		for line := range w.queue {
			func() {
				defer w.pending.Add(^uint64(0))
				if sinkFailed {
					w.drop(&w.causes.storage)
					return
				}
				if capBytes < trailerReserve || len(line) > capBytes-trailerReserve-written {
					w.drop(&w.causes.fileCap)
					return
				}
				n, err := sink.Write(line)
				written += n
				if err != nil || n != len(line) {
					w.drop(&w.causes.storage)
					// A failed write may have left a partial JSON object. Never append
					// another record (including the trailer) to that sink.
					sinkFailed = true
				}
			}()
		}
		if !sinkFailed && capBytes >= trailerReserve {
			b, _ := json.Marshal(Record{Version: 1, Record: "end", Sequence: w.sequence + 1, UTC: w.now().UTC().Format(time.RFC3339Nano), Dropped: w.dropped.Load(), DropCauses: w.causes.snapshot()}, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
			line := append(b, '\n')
			if n, err := sink.Write(line); err != nil || n != len(line) {
				w.drop(&w.causes.storage)
			}
		}
	}()
	return w
}

// SetLogger attaches the command's existing diagnostic sink before recording.
func (w *Writer) SetLogger(logger *slog.Logger) {
	if w != nil {
		w.logger.Store(logger)
	}
}

func (w *Writer) report(final bool) {
	logger := w.logger.Load()
	if logger == nil {
		return
	}
	n := w.Dropped()
	previous := w.lastCount.Load()
	now := time.Now().UnixNano()
	next := w.nextWarn.Load()
	if n == previous {
		return
	}
	if !final && (now < next || !w.nextWarn.CompareAndSwap(next, now+int64(time.Second))) {
		return
	}
	if !w.lastCount.CompareAndSwap(previous, n) {
		return
	}
	logger.Warn("diagnostics_lost", "rule", "trace_loss", "count", n, "causes", w.causes.snapshot())
}

// Now returns the trace edge monotonic elapsed clock. Pure cores still receive their owner clock.
func (w *Writer) Now() core.Instant { return core.Instant(time.Since(w.origin)) }

// SetServicePorts copies the device destinations from the proxy's port mapping.
// Call once before any recordings. Local listener, client and transport ports
// are never service ports, even when their number matches a mapped destination.
// At most 128 mappings are accepted by the proxy; excess entries remain aliased.
func (w *Writer) SetServicePorts(ports []uint16) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer func() { w.mu.Unlock(); w.report(false) }()
	if w.sequence != 0 || w.closed {
		w.drop(&w.causes.invalid)
		return errors.New("trace: service ports configured after recording or close")
	}
	var err error
	if len(ports) > 128 {
		w.drop(&w.causes.invalid)
		err = errors.New("trace: service port limit exceeded")
	}
	w.servicePorts = make(map[uint16]bool)
	for _, port := range ports[:min(len(ports), 128)] {
		if port == 0 {
			w.drop(&w.causes.invalid)
			err = errors.New("trace: zero service port omitted")
		}
		if port != 0 {
			w.servicePorts[port] = true
		}
	}
	return err
}

// Instance allocates an identity shared by all steps of one machine.
func (w *Writer) Instance() uint64 {
	if w == nil {
		return 0
	}
	return w.instances.Add(1)
}

func (w *Writer) drop(cause *atomic.Uint64) {
	cause.Add(1)
	w.dropped.Add(1)
	select {
	case w.failed <- struct{}{}:
	default:
	}
}

// Record copies only allowlisted summaries before buffers return to the owner.
// Admission losses count complete steps. A failed sink can leave one torn final
// record; the worker stops writing and discards every remaining queued step.
func (w *Writer) Record(machine string, instance uint64, now core.Instant, input, outputs any) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer func() {
		closed := w.closed
		w.mu.Unlock()
		if closed {
			w.report(false)
		}
	}()
	if w.closed {
		w.drop(&w.causes.closed)
		return
	}
	// Only fixed vocabulary can become a machine label.
	switch machine {
	case "iotc", "rdt", "tunnel", "session", "account", "allocation", "dtls", "lan":
	default:
		w.drop(&w.causes.invalid)
		return
	}
	out := reflect.ValueOf(outputs)
	if out.Kind() != reflect.Slice {
		w.drop(&w.causes.invalid)
		return
	}
	if out.Len() > 2048 {
		w.drop(&w.causes.transactionLimit)
		return
	}
	r := Record{Version: 1, Record: "step", Machine: machine, Instance: instance, MonoNS: now, UTC: w.now().UTC().Format(time.RFC3339Nano), Input: w.summarize(input), Dropped: w.dropped.Load()}
	if out.Len() > 0 {
		r.Outputs = make([]Summary, 0, out.Len())
	}
	for i := 0; i < out.Len(); i++ {
		r.Outputs = append(r.Outputs, w.summarize(out.Index(i).Interface()))
	}
	w.sequence++
	r.Sequence = w.sequence
	// Retain trace HTML/JS escapes; omitzero tags preserve v1 scalar omissions.
	b, err := json.Marshal(r, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	if err != nil {
		w.drop(&w.causes.invalid)
		return
	}
	if len(b) >= MaxLine {
		w.drop(&w.causes.transactionLimit)
		return
	}
	w.pending.Add(1)
	select {
	case w.queue <- append(b, '\n'):
	default:
		w.pending.Add(^uint64(0))
		w.drop(&w.causes.queueFull)
	}
}

// Dropped returns lost steps plus failed trailer writes and sink closes.
// A trailer cannot report its own write failure or a subsequent close failure;
// callers must inspect Dropped after Close for those final storage losses.
func (w *Writer) Dropped() uint64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// Close drains for at most two seconds after producers join. A stalled storage
// syscall may retain one worker and its bounded queue until storage recovers.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-w.done:
	case <-timer.C:
		if w.timedOut.CompareAndSwap(false, true) {
			n := max(1, w.pending.Load())
			w.dropped.Add(n)
			w.causes.storage.Add(n)
		}
		w.stopOnce.Do(func() { close(w.stop) })
		return errors.New("trace: drain timeout")
	}
	select {
	case <-w.reported:
	case <-timer.C:
		return errors.New("trace: diagnostic drain timeout")
	}
	w.report(true)
	if n := w.Dropped(); n > 0 {
		return fmt.Errorf("trace: %d diagnostics lost", n)
	}
	return nil
}

// Read decodes bounded JSON Lines through JSON v2. Unknown members are
// ignored for forward compatibility; incompatible schema versions fail closed.
func Read(in io.Reader) ([]Record, error) {
	s := bufio.NewScanner(io.LimitReader(in, SizeCap+1))
	s.Buffer(make([]byte, 4096), MaxLine)
	var records []Record
	total := 0
	for s.Scan() {
		total += len(s.Bytes()) + 1
		if total > SizeCap || len(records) >= 65536 {
			return nil, errors.New("trace: size cap exceeded")
		}
		var r Record
		if err := json.Unmarshal(s.Bytes(), &r); err != nil {
			return nil, err
		}
		if r.Version != 1 {
			return nil, errors.New("trace: unsupported version")
		}
		records = append(records, r)
	}
	return records, s.Err()
}

// RecordDTLS implements the packet adapter's consumer-defined trace seam. All
// arguments are scalar projections; payload buffers and errors never cross it.
func (w *Writer) RecordDTLS(instance uint64, kind uint8, inputLength, outputLength int, datatype uint16, outcome string) {
	if w == nil {
		return
	}
	w.Record("dtls", instance, w.Now(), EdgeEvent{Kind: kind, Length: inputLength, Type: datatype}, []EdgeEvent{{Kind: kind + 1, Length: outputLength, Outcome: outcome}})
}
