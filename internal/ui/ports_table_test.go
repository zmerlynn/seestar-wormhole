package ui

import (
	"strings"
	"testing"
)

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

// A Local port that follows its Seestar port claims it when typed.
func TestFollowedPortDuplicatePage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkFollowedPortDuplicate()")
}

// The page refuses a 129th port, counting Services rows that are off.
func TestPortLimitPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkPortLimit()")
}

// A refusal naming a port shows on its row.
func TestPortRefusalPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkPortRefusal()")
}

// A local port below 1024 shows the permissions help only on Linux.
func TestLowPortHelpPage(t *testing.T) {
	runPageRegression(t, "ports-table-test.js", "checkLowPortHelp()")
}

// The status names the server's system, for the low-port help.
func TestStatusNamesOS(t *testing.T) {
	saved := hostOS
	t.Cleanup(func() { hostOS = saved })
	for _, goos := range []string{"linux", "darwin", "windows"} {
		hostOS = goos
		s := testServer(t)
		if body := serve(s, request(s, "GET", "/api/status", "")).Body.String(); !strings.Contains(body, `"os":"`+goos+`"`) {
			t.Errorf("%s: status = %s", goos, body)
		}
	}
}
