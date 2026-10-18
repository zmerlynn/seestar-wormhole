package cloudhttp

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestCatalogExactModelIdentity(t *testing.T) {
	// Deliberately distinct literal labels: neither folding nor trimming is safe.
	const body = `{"data":{"total":5,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"one"},{"deviceSn":"private-a","deviceModel":"s50 pro","deviceStatus":1,"ssid":"two"},{"deviceSn":"private-a","deviceModel":" S50 Pro","deviceStatus":4,"ssid":"three"},{"deviceSn":"private-a","deviceModel":"S50 Pro ","deviceStatus":3,"ssid":"four"},{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"duplicate"}]}}`
	for _, model := range []string{"", "S50 Pro", "s50 pro", " S50 Pro", "S50 Pro "} {
		var logs bytes.Buffer
		f := &selectionCloud{bodies: []string{body}}
		c := selectionClient(t, f, &logs, slog.LevelDebug)
		got, err := c.SelectDevice(t.Context(), Device{Serial: "private-a", Model: model})
		if model == "" {
			var option *diagnostic.OptionError
			if got != (Device{}) || !errors.Is(err, cloud.ErrConfig) || !errors.As(err, &option) || option.Option != "--model" {
				t.Fatalf("selection=%v error=%v, want ambiguity with --model", got, err)
			}
		} else if err != nil || got != (Device{"private-a", model}) {
			t.Fatalf("selection=%v error=%v, want exact model", got, err)
		}
		assertCatalogPageRequests(t, f, 1)
		assertCatalogPagePrivacy(t, logs.String())
	}
}

func TestCatalogEmptyModelCannotHideAmbiguity(t *testing.T) {
	var logs bytes.Buffer
	f := &selectionCloud{bodies: []string{
		`{"data":{"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"one"},{"deviceSn":"private-a","deviceModel":"","deviceStatus":1,"ssid":"two"}]}}`,
	}}
	c := selectionClient(t, f, &logs, slog.LevelDebug)
	got, err := c.SelectDevice(t.Context(), Device{Serial: "private-a", Model: ""})
	if got != (Device{}) || !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("selection=%v error=%v, want invalid catalog without a selection", got, err)
	}
	assertCatalogPageRequests(t, f, 1)
	assertCatalogPagePrivacy(t, logs.String())
}
