//go:build gui && guitest

package ui

import _ "embed"

//go:embed testdata/page-state.js
var pageStateChecks string

func (d *guiTestDriver) pageStateRegressions() error {
	d.w.ExecJS(pageStateChecks)
	return d.probe(`typeof window.__guitestPageState === 'string' && !window.__guitestPageState.includes('FAIL') ? true : window.__guitestPageState || 'page state checks pending'`)
}
