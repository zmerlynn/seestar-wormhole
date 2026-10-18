package proxy

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"strings"
	"unicode"
)

// MaxSiteLine bounds inspected CRLF frames, including the terminator. Larger
// frames are rejected. This is local policy, not a device limit.
const MaxSiteLine = 16 << 10

type siteEvent struct {
	method    string
	id        any    // numeric request ID only; unexpected shapes never enter the log
	rejection []byte // non-nil means drop the request and reply locally
	reason    string
}

type siteRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rejectSite never includes request values in the error message or log event.
// Only a valid string/number ID is echoed to the requesting client.
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
	return nil, &siteEvent{method: method, id: siteLogID(envelope), reason: reason, rejection: append(response, '\r', '\n')}
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
		return rejectSite(nil, "", "frame_too_large", -32600)
	}
	if !bytes.HasSuffix(line, []byte("\r\n")) {
		return rejectSite(nil, "", "incomplete_frame", -32600)
	}
	// Exact keys match device dispatch. RawMessage maps retain unknown members,
	// including case variants, without rounding their numbers through float64.
	var envelope map[string]jsontext.Value
	var method string
	// V2 rejects all duplicate names, including nested and escaped spellings,
	// before any forwarding. R12 prohibits duplicate request members:
	// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/session-handshake.md#request-envelope
	if json.Unmarshal(line, &envelope) != nil || envelope == nil {
		return rejectSite(nil, "", "invalid_request", -32700)
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

// Keep arbitrary string IDs out of logs, while echoing them unchanged in replies.
func siteLogID(envelope map[string]jsontext.Value) any {
	var id *int64
	if json.Unmarshal(envelope["id"], &id) == nil && id != nil {
		return *id
	}
	return "unavailable"
}
