package cloudhttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestExchangeRawBodyLimit(t *testing.T) {
	for _, size := range []int{cloud.MaxBody, cloud.MaxBody + 1, cloud.MaxBody + 2} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			const response = `{"code":200}`
			body := response + strings.Repeat(" ", size-len(response))
			c := &Client{
				base: &url.URL{Scheme: "https", Host: "example.invalid"},
				transport: transportFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
				}),
			}
			// Test the capture guard independently: a later parser can otherwise
			// mask an HTTP reader that accepts or truncates an oversized body.
			_, got, err := c.exchangeRaw(t.Context(), "/stop", nil, nil, nil, false, false)
			if want := body[:min(size, cloud.MaxBody+1)]; string(got) != want {
				t.Fatalf("captured %d bytes, want the first %d bytes of the response", len(got), len(want))
			}
			if size == cloud.MaxBody {
				if err != nil {
					t.Fatalf("%d-byte response: got %v, want success", size, err)
				}
			} else if !errors.Is(err, cloud.ErrInvalid) {
				t.Fatalf("%d-byte response: got %v, want ErrInvalid", size, err)
			}
		})
	}
}
