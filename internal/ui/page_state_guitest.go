//go:build gui && guitest

package ui

import _ "embed"

//go:embed testdata/page-state.js
var pageStateChecks string

//go:embed testdata/change-telescope-test.js
var changeTelescopeChecks string

func (d *guiTestDriver) pageStateRegressions() error {
	d.w.ExecJS(changeTelescopeChecks + "\n" + pageStateChecks)
	return d.probe(`check(typeof window.__guitestPageState==='string' && !window.__guitestPageState.includes('FAIL'),{pageState:window.__guitestPageState ?? null})`)
}
