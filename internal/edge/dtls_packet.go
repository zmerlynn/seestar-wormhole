package edge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/transport/v5/deadline"
	profile "github.com/zmerlynn/seestar-wormhole/internal/dtls"
)

// ErrDTLSQueueFull reports bounded receive saturation. The packet transport may
// discard this datagram as loss; it must not terminate an established session.
var ErrDTLSQueueFull = errors.New("dtls: packet queue full")

// ErrDTLSBudget means lifetime control traffic exhausted the local storage budget.
var ErrDTLSBudget = errors.New("dtls: control traffic budget exhausted")

// DTLSTrace observes adapter decisions without a dependency on higher protocol
// layers. Callbacks receive only lengths, datatype and a fixed outcome category;
// no caller buffer or error object crosses this optional hook. Return promptly.
type DTLSTrace interface {
	RecordDTLS(instance uint64, kind uint8, inputLength, outputLength int, datatype uint16, outcome string)
}

// DTLSPackets connects Pion to IOTC datatype-1 datagrams without a UDP socket.
// The owner calls Deliver and drains Outgoing. Queues hold at most 32*1388 bytes
// each. There is one Pion reader/writer. Close unblocks reads and is idempotent.
// The mutex protects closure, application admission and control accounting; never held over I/O.
type DTLSPackets struct {
	trace                       DTLSTrace // immutable after worker startup
	traceID                     uint64
	mu                          sync.Mutex
	writeMu                     sync.Mutex
	receipts                    chan error
	closed                      bool
	applicationReady            bool
	controlBytes, controlCount  int
	mtu                         int
	rx, tx                      chan []byte
	done                        chan struct{}
	readDeadline, writeDeadline *deadline.Deadline
}

// NewDTLSPackets constructs a bounded packet path. MTU includes DTLS record
// overhead but excludes the IOTC envelope. 256 is a local minimum, 1388 the BIO cap.
func NewDTLSPackets(mtu int) (*DTLSPackets, error) {
	if mtu < 256 || mtu > profile.MaxDatagram {
		return nil, errors.New("dtls: invalid MTU")
	}
	return &DTLSPackets{mtu: mtu, rx: make(chan []byte, 32), tx: make(chan []byte, 32), done: make(chan struct{}), readDeadline: deadline.New(), writeDeadline: deadline.New()}, nil
}

// SetTrace installs the optional recorder before starting Pion or packet workers.
func (p *DTLSPackets) SetTrace(w DTLSTrace, id uint64) { p.trace = w; p.traceID = id }

// traceResult records existing adapter completions, outside its mutexes.
func (p *DTLSPackets) traceResult(kind uint8, inputLength, outputLength int, err error) {
	p.trace.RecordDTLS(p.traceID, kind, inputLength, outputLength, 0, dtlsOutcome(err))
}

// dtlsOutcome preserves the adapter's existing sanitized failure vocabulary.
func dtlsOutcome(err error) string {
	if err == nil {
		return "none"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "operation_failed"
}

// Deliver copies one complete IOTC payload. The caller must fail the attempt on
// error. Before authenticated handshake completion, application records are
// discarded, including those coalesced with control records (RFC 6347 section 4.2.4).
// Datatype 0 is simple data and is always rejected.
// Feed one IOTC payload as a datagram; simple data cannot bypass DTLS:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#encapsulation-and-role
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#secure-channel
func (p *DTLSPackets) Deliver(datatype byte, b []byte) (result error) {
	if p.trace != nil {
		inputLength := len(b)
		defer func() {
			p.trace.RecordDTLS(p.traceID, 0, inputLength, 0, uint16(datatype), dtlsOutcome(result))
		}()
	}
	if datatype != 1 {
		return errors.New("dtls: plaintext or unsupported datatype")
	}
	control, n, err := profile.DecodeDatagram(b)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return net.ErrClosed
	}
	// Pion bounds fragment storage, but its transcript cache has no exposed cap.
	// Bound all lifetime control input, including post-handshake messages, before
	// it enters the library. Policy permits ample retransmissions of this PSK profile.
	if n > 0 && (p.controlCount >= 512 || n > 256*1024-p.controlBytes) {
		return ErrDTLSBudget
	}
	// Pion's receive worker can block on its decrypted application queue before
	// it processes Finished. Drop early application records, preserving every
	// control record in order, using the same Pion decode as accounting above.
	// The queued payload is owned; never edit or retain the caller's buffer.
	var owned []byte
	if !p.applicationReady && n != len(b) {
		if n == 0 {
			return nil
		}
		owned = make([]byte, 0, n)
		for _, record := range control {
			owned = append(owned, record...)
		}
	} else {
		owned = bytes.Clone(b)
	}
	select {
	case p.rx <- owned:
		if n > 0 {
			p.controlBytes += n
			p.controlCount++
		}
		return nil
	default:
		return ErrDTLSQueueFull
	}
}

// allowApplication is called only after the library authenticates the handshake.
func (p *DTLSPackets) allowApplication() {
	if p.trace != nil {
		defer p.traceResult(10, 0, 0, nil)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applicationReady = true
}

// Outgoing transfers owned payloads to IOTC; send each with datatype 1, partial
// encryption enabled, and its existing session/channel identity. Never concatenate.
// This channel is not closed; use Done to terminate the owner pump.
func (p *DTLSPackets) Outgoing() <-chan []byte { return p.tx }

// Done closes when the adapter is closed.
func (p *DTLSPackets) Done() <-chan struct{} { return p.done }
func (p *DTLSPackets) ReadFrom(b []byte) (n int, addr net.Addr, result error) {
	if p.trace != nil {
		inputLength := len(b)
		defer func() { p.traceResult(6, inputLength, n, result) }()
	}
	select {
	case <-p.done:
		return 0, nil, net.ErrClosed
	case <-p.readDeadline.Done():
		return 0, nil, os.ErrDeadlineExceeded
	default:
	}
	select {
	case <-p.done:
		return 0, nil, net.ErrClosed
	case <-p.readDeadline.Done():
		return 0, nil, os.ErrDeadlineExceeded
	case d := <-p.rx:
		if len(b) < len(d) {
			return 0, nil, io.ErrShortBuffer
		}
		return copy(b, d), dtlsAddr{}, nil
	}
}
func (p *DTLSPackets) WriteTo(b []byte, _ net.Addr) (n int, result error) {
	if p.trace != nil {
		inputLength := len(b)
		defer func() { p.traceResult(4, inputLength, n, result) }()
	}
	if len(b) == 0 || len(b) > p.mtu {
		return 0, errors.New("dtls: packet exceeds MTU")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	select {
	case <-p.done:
		return 0, net.ErrClosed
	case <-p.writeDeadline.Done():
		return 0, os.ErrDeadlineExceeded
	default:
	}
	select {
	case p.tx <- bytes.Clone(b):
	case <-p.done:
		return 0, net.ErrClosed
	case <-p.writeDeadline.Done():
		return 0, os.ErrDeadlineExceeded
	}
	if p.receipts != nil {
		select {
		case err := <-p.receipts:
			if err != nil {
				return 0, err
			}
		case <-p.done:
			return 0, net.ErrClosed
		}
	}
	return len(b), nil
}

// EnableWriteCompletion makes each write wait for the IOTC socket completion.
// Configure before starting Pion. CompleteWrite is called by the nonblocking owner.
func (p *DTLSPackets) EnableWriteCompletion() { p.receipts = make(chan error, 1) }
func (p *DTLSPackets) CompleteWrite(err error) {
	if p.trace != nil {
		defer p.traceResult(8, 0, 0, err)
	}
	select {
	case p.receipts <- err:
	default:
	}
}

func (p *DTLSPackets) Close() error {
	if p.trace != nil {
		defer p.traceResult(12, 0, 0, nil)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.done)
		p.readDeadline.Set(time.Time{})
		p.writeDeadline.Set(time.Time{})
		// Queues remain bounded until the owner releases this adapter. Draining
		// here would race an active consumer and could block shutdown.
	}
	return nil
}
func (*DTLSPackets) LocalAddr() net.Addr                  { return dtlsAddr{} }
func (p *DTLSPackets) SetDeadline(t time.Time) error      { return p.setDeadlines(t, true, true) }
func (p *DTLSPackets) SetReadDeadline(t time.Time) error  { return p.setDeadlines(t, true, false) }
func (p *DTLSPackets) SetWriteDeadline(t time.Time) error { return p.setDeadlines(t, false, true) }
func (p *DTLSPackets) setDeadlines(t time.Time, read, write bool) error {
	if p.trace != nil {
		defer p.traceResult(14, 0, 0, nil)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return net.ErrClosed
	}
	if read {
		p.readDeadline.Set(t)
	}
	if write {
		p.writeDeadline.Set(t)
	}
	return nil
}

type dtlsAddr struct{}

func (dtlsAddr) Network() string { return "iotc" }
func (dtlsAddr) String() string  { return "iotc-dtls" }
