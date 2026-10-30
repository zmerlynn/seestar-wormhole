package ui

import "net/http"

// The page is served exclusively by our authenticated loopback server. Never
// dispatch a request to Wails' separate asset server or runtime transport.
func blockWindowAssets(_ http.Handler) http.Handler { return http.NotFoundHandler() }
