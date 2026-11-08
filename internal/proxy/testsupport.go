package proxy

import (
	"context"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// WireRunner is a device-side transport a test supplies in place of IOTC.
type WireRunner interface {
	Run(context.Context, <-chan iotc.Action, func(iotc.Event) bool)
}

// StartWithWire starts a Session whose cloud, device, license check and
// wire are supplied. Test support only: no command calls it.
//
// Like New, it publishes the device into cfg.Status before the session
// starts, and the session owns client: StartWithWire closes it on failure.
func StartWithWire(ctx context.Context, cfg Config, client *cloudhttp.Client, device cloudhttp.Device, verify func(string) (iotc.License, error), wire func(iotc.License) WireRunner) (*Session, error) {
	cfg.Status.SetSelectedDevice(device)
	s, err := start(ctx, cfg, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: client, device: device, verify: verify, wire: wire}
	})
	if err != nil {
		client.Close()
		return nil, err
	}
	return s, nil
}
