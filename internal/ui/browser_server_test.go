package ui

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// TestBrowserServer serves the app's page without a window, for the
// Playwright tests in test/browser. It runs only when
// SEESTAR_WORMHOLE_BROWSER_STATE names its state folder, prints the page's
// private URL as a browser run does, and stops on an interrupt or the
// page's Quit. Nothing reaches an account: the keychain is empty and a
// Connect's proxy waits to be stopped. Nothing scans the host's network
// either: scans and Test's probe go to an empty fake network, so a probe
// ends with its short window wherever the tests run.
func TestBrowserServer(t *testing.T) {
	root := os.Getenv("SEESTAR_WORMHOLE_BROWSER_STATE")
	if root == "" {
		t.Skip("set SEESTAR_WORMHOLE_BROWSER_STATE to serve the page for test/browser")
	}
	useLAN(t, &lanNet{})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	run := func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status, _ func(proxy.Dialer) (func(), bool)) error {
		<-ctx.Done()
		return ctx.Err()
	}
	noBrowser := func(string) error { return errors.New("no browser") }
	if err := runUIPresented(ctx, root, os.Stdout, os.Stderr, run, noBrowser, &fakeKeychain{}, nil, nil); err != nil {
		t.Fatal(err)
	}
}
