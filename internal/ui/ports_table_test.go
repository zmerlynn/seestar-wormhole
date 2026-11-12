package ui

import "testing"

// The Services rows are in Seestar port order.
func TestPortOrderPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkPortOrder()")
}

// Rows whose local port differs from the Seestar port are shaded, live.
func TestPortShadingPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkPortShading()")
}

// Added rows put the Local port first.
func TestAddedPortColumnsPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkAddedColumns()")
}

// Port fields show their own errors as they are edited.
func TestPortRowErrorsPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkPortRowErrors()")
}

// A refusal naming a port shows on its row.
func TestPortRefusalPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkPortRefusal()")
}
