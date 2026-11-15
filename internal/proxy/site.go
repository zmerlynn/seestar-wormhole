package proxy

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"
	"strings"
	"unicode"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// MaxSiteLine bounds inspected CRLF frames, including the terminator. Larger
// frames are rejected. This is local policy, not a device limit.
const MaxSiteLine = 16 << 10

type siteEvent struct {
	method    string
	id        any    // the request ID as sent: an integer, a string or a number's literal
	params    string // the setter's params as siteParams projects them; empty when absent
	rejection []byte // non-nil means drop the request and reply locally
	reason    string
}

type siteRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rejectSite keeps request values out of the reply's error message; the log
// event carries the ID and params. Only a valid string/number ID is echoed.
// These local JSON-RPC error objects deliberately differ from the device flat code/error envelope:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/errors.md#errors-on-the-wire
func rejectSite(envelope map[string]jsontext.Value, method, reason string, code int) ([]byte, *siteEvent) {
	id := jsontext.Value("null")
	raw := envelope["id"]
	// Validate the raw scalar without converting large numeric IDs to float64.
	if raw.IsValid() && (raw.Kind() == '"' || raw.Kind() == '0') {
		id = raw
	}
	response, _ := json.Marshal(struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      jsontext.Value `json:"id"`
		Error   siteRPCError   `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: siteRPCError{code, siteErrorMessage(reason)}}, jsontext.PreserveRawStrings(true), jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	return nil, &siteEvent{method: method, id: siteLogID(envelope), params: siteParams(envelope["params"]), reason: reason, rejection: append(response, '\r', '\n')}
}

// siteRecoveredID lets the JSON decoder collect a complete scalar even when a
// later member is malformed. Repeated IDs are ambiguous, including escaped names.
type siteRecoveredID struct {
	raw  jsontext.Value
	seen bool
	end  int64
}

func (id *siteRecoveredID) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	repeated := id.seen
	id.seen = true
	if repeated {
		id.raw = nil
	}
	raw, err := dec.ReadValue()
	if err == nil && !repeated && (raw.Kind() == '"' || raw.Kind() == '0') {
		id.raw = raw.Clone()
		id.end = dec.InputOffset()
	}
	return err
}

// Recovery is only for a refusal's correlation, never for forwarding. The
// library handles all JSON traversal; allowing duplicates here lets an ID after
// unrelated duplicate members survive without relaxing the protection decoder.
func recoverSiteEnvelope(line []byte) map[string]jsontext.Value {
	line = line[:min(len(line), MaxSiteLine)]
	type envelope struct {
		ID siteRecoveredID `json:"id"`
	}
	var request envelope
	if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []envelope
		_ = json.Unmarshal(line, &batch, jsontext.AllowDuplicateNames(true))
		if len(batch) != 1 {
			return nil // Multiple requests have no single correlation ID.
		}
		request = batch[0]
	} else {
		_ = json.Unmarshal(line, &request, jsontext.AllowDuplicateNames(true))
	}
	id := request.ID
	if id.raw.Kind() == '0' && len(line) == MaxSiteLine && id.end == int64(len(line)) {
		return nil // The inspection bound may have cut a numeric ID in two.
	}
	return map[string]jsontext.Value{"id": id.raw}
}

// CRLF-only, object-only and 16-KiB limits are stricter local inspection policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/ports.md#framing-per-port-family
func protectSite(line []byte) ([]byte, *siteEvent) {
	// Empty separators cannot dispatch a setter. Everything else must be safely
	// decoded before it is forwarded, including bare-LF and EOF fragments.
	if len(bytes.TrimSpace(line)) == 0 {
		return line, nil
	}
	if len(line) > MaxSiteLine {
		return rejectSite(recoverSiteEnvelope(line), "", "frame_too_large", -32600)
	}
	if !bytes.HasSuffix(line, []byte("\r\n")) {
		return rejectSite(recoverSiteEnvelope(line), "", "incomplete_frame", -32600)
	}
	// Exact keys match device dispatch. RawMessage maps retain unknown members,
	// including case variants, without rounding their numbers through float64.
	var envelope map[string]jsontext.Value
	var method string
	// V2 rejects all duplicate names, including nested and escaped spellings,
	// before any forwarding. R12 prohibits duplicate request members:
	// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/session-handshake.md#request-envelope
	if json.Unmarshal(line, &envelope) != nil || envelope == nil {
		return rejectSite(recoverSiteEnvelope(line), "", "invalid_request", -32700)
	}
	if json.Unmarshal(envelope["method"], &method) != nil || strings.ContainsFunc(method, unicode.IsControl) {
		return rejectSite(envelope, "", "invalid_method", -32600)
	}
	// Drop these guider setters even when the imager would relay them:
	// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#user-location
	// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#setting-the-time-pi_set_time
	// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#mount-time
	// https://github.com/zmerlynn/seestar-spec/blob/v1/catalog/guider/methods/set_user_location.yaml
	// https://github.com/zmerlynn/seestar-spec/blob/v1/catalog/guider/methods/pi_set_time.yaml
	// https://github.com/zmerlynn/seestar-spec/blob/v1/catalog/guider/methods/scope_set_location.yaml
	// https://github.com/zmerlynn/seestar-spec/blob/v1/catalog/guider/methods/scope_set_time.yaml
	switch method {
	case "set_user_location", "pi_set_time", "scope_set_location", "scope_set_time":
		return rejectSite(envelope, method, "site_protected", -32602)
	default:
		return line, nil
	}
}

func siteErrorMessage(reason string) string {
	if reason == "site_protected" {
		return reason
	}
	return "request rejected by site protection"
}

// siteParamFields are the setters' coordinate, clock and zone members:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#setting-the-time-pi_set_time
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#mount-time
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/time-and-location.md#user-location
var siteParamFields = map[string]bool{
	"lat": true, "lon": true, "latitude": true, "longitude": true, "time_zone": true,
	"year": true, "mon": true, "day": true, "hour": true, "min": true, "sec": true, "offset": true, "utc": true,
}

// siteParams projects a dropped setter's params for its log record: the
// coordinate, clock and zone members with their scalar values, positional
// scalars, and only the names of other members, listed under "unknown".
// Unknown values, and nested arrays, are never copied: they could carry an
// access secret. The result is clipped to keep the record within a log
// sink's limit.
func siteParams(raw jsontext.Value) string {
	if !raw.IsValid() {
		return ""
	}
	projected := siteParamObject(raw)
	if raw.Kind() == '[' {
		var elements []jsontext.Value
		_ = json.Unmarshal(raw, &elements)
		positional := make([]any, len(elements))
		for i, element := range elements {
			positional[i] = siteParamObject(element)
			if element.Kind() == '[' {
				positional[i] = []any{}
			}
		}
		projected = positional
	}
	b, _ := json.Marshal(projected, json.Deterministic(true))
	return diagnostic.Clip(string(b), diagnostic.MaxValue)
}

// siteParamObject projects an object's members; any other value is a scalar
// and is returned as sent.
func siteParamObject(raw jsontext.Value) any {
	if raw.Kind() != '{' {
		return raw
	}
	var members map[string]jsontext.Value
	_ = json.Unmarshal(raw, &members)
	projected := map[string]any{}
	var unknown []string
	for name, value := range members {
		if siteParamFields[name] && value.Kind() != '{' && value.Kind() != '[' {
			projected[name] = value
		} else {
			unknown = append(unknown, name)
		}
	}
	if unknown != nil {
		slices.Sort(unknown)
		projected["unknown"] = unknown
	}
	return projected
}

// siteLogID logs an integer or string ID as its value and any other number as
// its literal, so an ID too large for int64 is not rounded. A string or
// literal is clipped.
func siteLogID(envelope map[string]jsontext.Value) any {
	raw := envelope["id"]
	var id int64
	var text string
	switch {
	case raw.Kind() == '0' && json.Unmarshal(raw, &id) == nil:
		return id
	case raw.Kind() == '0':
		return diagnostic.Clip(string(raw), diagnostic.MaxValue)
	case raw.Kind() == '"' && json.Unmarshal(raw, &text) == nil:
		return diagnostic.Clip(text, diagnostic.MaxValue)
	}
	return "unavailable"
}
