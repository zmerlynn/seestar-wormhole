package cloudhttp

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// newAuthenticatedFixture isolates allocation/transport tests from account login.
// These synthetic credentials model the output of a successful account owner.
func newAuthenticatedFixture(t *testing.T, cfg Config) (*Client, error) {
	t.Helper()
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api-ithings.zwoastro.com"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.StopTimeout == 0 {
		cfg.StopTimeout = 5 * time.Second
	}
	if cfg.Transport == nil {
		cfg.Transport = &http.Transport{DisableKeepAlives: true}
	}
	return &Client{trace: cfg.Trace, logger: cfg.Logger, base: u, transport: cfg.Transport, token: "token-secret", source: "source-secret", timeout: cfg.Timeout, stopTimeout: cfg.StopTimeout}, nil
}
