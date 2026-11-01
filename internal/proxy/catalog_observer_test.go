package proxy

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
)

type constructorCatalogCloud struct {
	pairAllocationCloud
	failFirst bool
	lists     int
}

func (c *constructorCatalogCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/my-device" {
		c.lists++
		if c.failFirst && c.lists == 1 {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"code":503}`))}, nil
		}
	}
	return c.pairAllocationCloud.RoundTrip(r)
}

func TestConstructorCatalogObserver(t *testing.T) {
	const one = `{"deviceSn":"synthetic-catalog-one","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}`
	const two = `{"deviceSn":"synthetic-catalog-two","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic"}`
	for _, tc := range []struct {
		name          string
		refuse, retry bool
	}{
		{name: "success"}, {name: "selection refusal", refuse: true}, {name: "retry success", retry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				list := `{"data":{"total":1,"items":[` + one + `]}}`
				pairs := []cloudhttp.CatalogPair{{Model: "S30", Serial: "synthetic-catalog-one"}}
				if tc.refuse {
					list = `{"data":{"total":2,"items":[` + one + `,` + two + `]}}`
					pairs = append(pairs, cloudhttp.CatalogPair{Model: "S50 Pro", Serial: "synthetic-catalog-two"})
				}
				peer := &constructorCatalogCloud{pairAllocationCloud: pairAllocationCloud{t: t, list: list}, failFirst: tc.retry}
				var logs bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&logs, nil))
				client, err := newFixtureClient(t, cloudhttp.Config{Transport: peer, Logger: logger})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				status := NewStatus()
				var catalogs []cloudhttp.Catalog
				status.Catalog = func(c cloudhttp.Catalog) {
					if status.SelectedDevice() != "" || status.Text() != "Connecting" {
						t.Error("catalog published after selected/status identity")
					}
					if strings.Contains(logs.String(), `"msg":"device_selected"`) {
						t.Error("catalog published after selection log")
					}
					select {
					case <-status.Updates():
						t.Error("status update published before catalog")
					default:
					}
					catalogs = append(catalogs, c)
				}
				s, err := newWithClient(t.Context(), Config{Idle: time.Minute, Status: status, Logger: logger}, client)
				if tc.refuse {
					if !errors.Is(err, cloud.ErrConfig) || s != nil {
						t.Fatalf("selection refusal: session=%v err=%v", s, err)
					}
					if status.SelectedDevice() != "" {
						t.Fatal("refusal published selected identity")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					if status.SelectedDevice() != "S30 synthetic-catalog-one" {
						t.Fatal("success lost selected identity")
					}
				}
				expected := []cloudhttp.Catalog{{Pairs: pairs, Count: len(pairs), Complete: true}}
				if tc.retry {
					expected = append([]cloudhttp.Catalog{{Pairs: []cloudhttp.CatalogPair{}}}, expected...)
				}
				if !reflect.DeepEqual(catalogs, expected) {
					t.Fatalf("constructor catalogs=%+v want=%+v", catalogs, expected)
				}
				if peer.lists != len(expected) {
					t.Fatalf("catalog fetches=%d want=%d", peer.lists, len(expected))
				}
				selected := strings.Count(logs.String(), `"msg":"device_selected"`)
				if (tc.refuse && selected != 0) || (!tc.refuse && selected != 1) {
					t.Fatalf("selected records=%d", selected)
				}
				if peer.allocs.Load() != 0 || peer.details.Load() != 0 {
					t.Fatal("lazy constructor allocated or queried device detail")
				}
			})
		})
	}
}
