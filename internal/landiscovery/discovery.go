// Package landiscovery answers app scans with a bounded, single-owner core and
// a separate UDP edge.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery.md
package landiscovery

import (
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"time"
)

// Port is the app's discovery port; MaxDatagram bounds the receive buffer.
const Port = 4720
const MaxDatagram = 4096

// ReplyBurst permits four immediate replies, then one per second globally.
// This is local anti-amplification policy, not an observed device timer.
const ReplyBurst = 4

var errInvalid = errors.New("LAN discovery: invalid request or device detail")
var errUnknownMethod = errors.New("LAN discovery: unknown method")

type request struct {
	ID     jsontext.Value
	Method string
	Code   int
}

// Native dispatcher codes; bounded whole-datagram object decoding is local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#device-side-responder
func decodeRequest(b []byte) (request, error) {
	var r request
	if len(b) > MaxDatagram {
		return r, errInvalid
	}
	// Only the dispatcher fields are consumed. Params and platform-specific
	// root members are ignored by the scan handler, regardless of their type.
	var v *struct {
		ID     jsontext.Value `json:"id"`
		Method jsontext.Value `json:"method"`
	}
	if json.Unmarshal(b, &v) != nil || v == nil {
		return r, errInvalid
	}
	r.ID = v.ID
	var method *string
	if err := json.Unmarshal(v.Method, &method); err != nil {
		method = nil
	}
	if method != nil {
		r.Method = *method
	}
	switch {
	case len(r.ID) == 0:
		r.ID, r.Code = jsontext.Value("null"), 200
	case method == nil:
		r.Code = 100
	case r.Method != "scan_iscope":
		return request{}, errUnknownMethod // Native unknown-method scans stay unanswered.
	}
	return r, nil
}

// The synthesized reply carries only cloud identity and the inferred activation bit.
// omitzero omits nil pointers while retaining nonnil empty/false values as v1 did.
type result struct {
	SN           *string `json:"sn,omitzero"`
	SSID         *string `json:"ssid,omitzero"`
	ProductModel *string `json:"product_model,omitzero"`
	Verified     *bool   `json:"is_verified,omitzero"`
}

type reply struct {
	JSONRPC string `json:"jsonrpc"`
	// The linked JRpcResponse constructor spells this "Timestamp"; its linked
	// getTimestamp uses CLOCK_MONOTONIC, "%ld.%09ld". We synthesize proxy elapsed
	// time in that format, not a claimed real device timestamp.
	Timestamp string         `json:"Timestamp"`
	Method    string         `json:"method"`
	ID        jsontext.Value `json:"id"`
	Code      int            `json:"code"`
	Result    *result        `json:"result,omitzero"`
}

// Machine owns immutable reply data and a constant-size global token bucket.
// Step is called by one owner with monotonic elapsed time; it performs no I/O.
type Machine struct {
	result   result
	tokens   int
	last     time.Duration
	refusals [refusalCount]uint64
}

// New synthesizes a discovery reply from cloud identity. Synthesized names may
// differ from device-reported values.
// Cloud-derived identity and activation=true are local synthesis, not observed device values:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#stand-in-responders
func New(serial, name, model string) (*Machine, []string, error) {
	var r result
	if serial == "" {
		return nil, nil, errInvalid
	}
	r.SN = &serial
	var synthesized []string
	if name == "" {
		name = model
		synthesized = append(synthesized, "ssid")
	}
	r.SSID = &name
	if model != "" {
		// Cloud labels are not guaranteed to match firmware product_model spelling.
		r.ProductModel = &model
		synthesized = append(synthesized, "product_model")
	}
	// The inferred minimal reply uses true; cloud status does not establish
	// hardware activation. Log this assumption.
	verified := true
	r.Verified = &verified
	synthesized = append(synthesized, "is_verified")
	m := &Machine{result: r, tokens: ReplyBurst}
	// Account for the widest ID and timestamp and JSON escaping at startup.
	if m.encode(request{ID: jsontext.Value("-9223372036854775808"), Method: "scan_iscope"}, time.Duration(1<<63-1)) == nil {
		return nil, nil, errInvalid
	}
	return m, synthesized, nil
}

// Reply is a whole datagram without a terminator, addressed back to the requester:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#reply
func (m *Machine) encode(r request, now time.Duration) []byte {
	// Replies omit line endings before unicast. Error replies carry only the
	// dispatcher outcome, never synthesized device state as a successful result.
	var result *result
	if r.Code == 0 {
		result = &m.result
	}
	// Preserve reply bounds and wire bytes: prior HTML/JS escaping and raw ID spelling.
	b, _ := json.Marshal(reply{"2.0", fmt.Sprintf("%d.%09d", now/time.Second, now%time.Second), r.Method, r.ID, r.Code, result}, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true), jsontext.PreserveRawStrings(true))
	if len(b) > MaxDatagram {
		return nil
	}
	return b
}

// Step ignores malformed/oversize scans and returns at most one bounded reply.
// Clock regression cannot refill the rate budget. Malformed scans cost no token.
func (m *Machine) Step(b []byte, now time.Duration) []byte {
	r, ok := m.prepare(b, now)
	if !ok {
		return nil
	}
	return m.reply(r, now)
}

// prepare validates and refills without spending. The single owner can perform
// live-source admission before committing an eligible request with reply.
func (m *Machine) prepare(b []byte, now time.Duration) (request, bool) {
	r, err := decodeRequest(b)
	if err != nil {
		rule := refusalMalformed
		if len(b) > MaxDatagram {
			rule = refusalOversize
		} else if err == errUnknownMethod {
			rule = refusalUnknownMethod
		}
		m.refuse(rule)
		return request{}, false
	}
	now = max(now, m.last)
	delta := now - m.last
	if delta >= time.Second {
		m.tokens = min(ReplyBurst, m.tokens+int(min(delta/time.Second, ReplyBurst)))
		m.last = now - delta%time.Second
	}
	if m.tokens == 0 {
		m.refuse(refusalRate)
		return request{}, false
	}
	return r, true
}

// reply commits a prepared request after admission; calls share one owner.
func (m *Machine) reply(r request, now time.Duration) []byte {
	m.tokens--
	b := m.encode(r, max(now, m.last))
	if b == nil {
		m.refuse(refusalOutput)
	} else if r.Code == 200 {
		m.refuse(refusalMissingID)
	} else if r.Code == 100 {
		m.refuse(refusalInvalidMethod)
	}
	return b
}
