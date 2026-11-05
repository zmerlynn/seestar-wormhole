// Package localhttp is the policy every loopback page and API in the app
// shares: the response headers, the JSON a response writes and the size of
// a JSON request.
package localhttp

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
)

// MaxJSON caps a JSON request body and a settings file.
const MaxJSON = 64 << 10

// Policy is the Content-Security-Policy of a page framed only by its own
// origin.
const Policy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"

// policyUnframed is Policy for a page no origin may frame.
const policyUnframed = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// SetHeaders sets the headers of every response: the CSP, framing by the
// same origin only (or by none when unframed), no referrer, no caching, no
// sniffing and no cross-origin reads.
func SetHeaders(h http.Header, unframed bool) {
	policy, frames := Policy, "SAMEORIGIN"
	if unframed {
		policy, frames = policyUnframed, "DENY"
	}
	h.Set("Content-Security-Policy", policy)
	h.Set("X-Frame-Options", frames)
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}

// respondOptions write a nil slice as [] and a nil map as {}, sort map
// members, escape HTML characters, U+2028 and U+2029, and write a byte that
// is not UTF-8 as U+FFFD.
var respondOptions = json.JoinOptions(json.Deterministic(true),
	jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true), jsontext.AllowInvalidUTF8(true))

// Respond writes v as JSON with the status code.
func Respond(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v, respondOptions)
	if err != nil {
		http.Error(w, "Response unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(b)
}
