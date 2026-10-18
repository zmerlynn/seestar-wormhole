package cloudhttp

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"testing"
)

func TestEnvironmentAttributionKeepsAcceptedValues(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "a+b@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "<> &'=😀123")
	c, err := NewFromEnvironment(Config{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
func TestDeviceInputAttribution(t *testing.T) {
	c := &Client{}
	_, err := c.DetailForLAN(t.Context(), Device{Serial: "private\r", Model: "S50 Pro"})
	var option *diagnostic.OptionError
	if !errors.Is(err, cloud.ErrConfig) || !errors.As(err, &option) || option.Option != "--serial" || option.Rule != SerialRule {
		t.Fatal(err)
	}
	_, err = c.DetailForLAN(t.Context(), Device{Serial: "serial", Model: "private\r"})
	if !errors.Is(err, cloud.ErrInvalid) || errors.As(err, &option) {
		t.Fatal(err)
	}
}
