package localhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRespond(t *testing.T) {
	w := httptest.NewRecorder()
	Respond(w, http.StatusConflict, struct {
		Slice []string          `json:"slice"`
		Map   map[string]string `json:"map"`
		Text  string            `json:"text"`
	}{Text: "<>&\u2028\u2029\xff"})
	want := `{"slice":[],"map":{},"text":"\u003c\u003e\u0026\u2028\u2029` + "\ufffd" + `"}`
	if got := w.Body.String(); got != want || w.Code != http.StatusConflict || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Respond wrote %d %q\n%s\nwant\n%s", w.Code, w.Header().Get("Content-Type"), got, want)
	}
	w = httptest.NewRecorder()
	Respond(w, 200, func() {})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("an unencodable value = %d", w.Code)
	}
}

func TestSetHeaders(t *testing.T) {
	for _, unframed := range []bool{false, true} {
		h := http.Header{}
		SetHeaders(h, unframed)
		frames, ancestors := "SAMEORIGIN", "frame-ancestors 'self'"
		if unframed {
			frames, ancestors = "DENY", "frame-ancestors 'none'"
		}
		for k, want := range map[string]string{"X-Frame-Options": frames, "Cross-Origin-Resource-Policy": "same-origin",
			"Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store"} {
			if got := h.Get(k); got != want {
				t.Errorf("unframed %v: %s = %q, want %q", unframed, k, got, want)
			}
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, ancestors+";") || strings.Count(csp, "frame-ancestors") != 1 ||
			strings.Replace(csp, ancestors, "frame-ancestors 'self'", 1) != Policy {
			t.Errorf("unframed %v: CSP %q", unframed, csp)
		}
	}
}
