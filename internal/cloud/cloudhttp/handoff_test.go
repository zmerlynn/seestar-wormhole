package cloudhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Body.Close runs after exchange's last context check but before Acquire's
// ownership handoff. Cancel here to exercise the final guard deterministically.
func TestCancellationBeforeAllocationHandoff(t *testing.T) {

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stops, closes int
	c, err := newAuthenticatedFixture(t, Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "stop") {
			stops++
			if r.Context().Err() != nil {
				t.Error("cleanup inherited canceled attempt")
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"conId":"allocation-secret"}` {
				t.Errorf("cleanup body=%q", b)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":null,"msg":"ok"}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: &onCloseBody{Reader: strings.NewReader(online), close: func() { closes++; cancel() }}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lease, err := c.Acquire(ctx, Device{Serial: "serial", Model: "S50 Pro"})
	if lease != nil {
		defer lease.Close()
	}
	if !errors.Is(err, context.Canceled) || lease != nil || closes != 1 || stops != 1 {
		t.Fatalf("canceled handoff: err=%v lease=%t closes=%d stops=%d, want Canceled, no lease, 1 close, 1 stop", err, lease != nil, closes, stops)
	}
}

type onCloseBody struct {
	io.Reader
	close func()
}

func (b *onCloseBody) Close() error { b.close(); return nil }
