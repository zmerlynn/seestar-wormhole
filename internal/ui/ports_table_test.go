package ui

import "testing"

// The Services rows are in Seestar port order.
func TestPortOrderPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkPortOrder()")
}
