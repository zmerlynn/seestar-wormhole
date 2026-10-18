// Package capturetrace decodes bounded offline wire observations into the
// tracerecord allowlist. It never reconstructs Steps or internal actions.
package capturetrace

import (
	"errors"
	"fmt"
	"io"
	"math"
)

// Limits bounds input bytes (also expanded packet bytes), packet count,
// directional flows, authenticated records, encoded output and keylog bytes.
// Zero selects defaults; negative values and MaxInt byte limits are invalid.
type Limits struct{ CaptureBytes, Packets, Flows, DecryptedRecords, OutputBytes, KeylogBytes int }

// Options selects exactly one attempt's master key (reference 3). The key is
// never inferred from addresses or a local runtime identity.
type Options struct {
	Attempt uint64
	Limits  Limits
}

// Result contains only redacted wire observations. Incomplete output has a loss
// trailer, so tracediff cannot claim equality even if a caller ignores the error.
// An output cap too small for a trailer returns no JSONL. Internal runtime
// observations are unavailable; compare only a corresponding wire-only trace.
type Result struct {
	JSONL      []byte
	Incomplete bool
}

// LimitError identifies an exhausted bound without input text.
type LimitError struct {
	Resource string
	Limit    int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("capture trace: %s limit %d exceeded", e.Resource, e.Limit)
}

// Failure is a fixed diagnostic category; no source error text is retained.
type Failure string

const (
	MalformedCapture  Failure = "malformed capture"
	UnsupportedLink   Failure = "unsupported link"
	UnsupportedLayer  Failure = "unsupported layer"
	MalformedVendor   Failure = "malformed vendor framing"
	UnsupportedRecord Failure = "unsupported record"
	MissingKey        Failure = "missing key"
	MalformedKeylog   Failure = "malformed keylog"
	Authentication    Failure = "authentication failed"
)

// DecodeError identifies a failure and the one-based capture packet, or zero
// before packet decoding. It never includes capture, keylog or library errors.
type DecodeError struct {
	Kind   Failure
	Packet int
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("capture trace: %s at packet %d", e.Kind, e.Packet)
}

func defaults(l Limits) (Limits, error) {
	for _, p := range []struct {
		v *int
		d int
	}{
		{&l.CaptureBytes, 16 << 20}, {&l.Packets, 65536}, {&l.Flows, 128},
		{&l.DecryptedRecords, 32768}, {&l.OutputBytes, 1 << 20}, {&l.KeylogBytes, 64 << 10},
	} {
		if *p.v == 0 {
			*p.v = p.d
		}
		if *p.v < 1 || *p.v == math.MaxInt {
			return l, errors.New("capture trace: invalid limits")
		}
	}
	return l, nil
}

func bounded(r io.Reader, n int, resource string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(n)+1))
	if len(b) > n {
		return nil, &LimitError{resource, n}
	}
	if err != nil {
		return nil, &DecodeError{Kind: MalformedCapture}
	}
	return b, nil
}
