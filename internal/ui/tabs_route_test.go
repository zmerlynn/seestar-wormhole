package ui

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestTabsPage(t *testing.T) {
	runPageRegression(t, "tabs-route-test.js", "checkTabs()")
}

func TestActivityHoldsDetails(t *testing.T) {
	runPageRegression(t, "tabs-route-test.js", "checkActivityMove()")
}

// The page shows a refused copy.json's note from the server's own status.
func TestCopyRefusedPage(t *testing.T) {
	s := testServer(t)
	s.copyRefused = true
	status := serve(s, request(s, "GET", "/api/status", "")).Body.String()
	if !strings.Contains(status, `"copy_refused":true`) {
		t.Fatalf("status = %s", status)
	}
	runPageRegression(t, "tabs-route-test.js", "checkCopyRefused("+status+")")
}

// routeStatuses is the server's own status before and after a LAN save.
func routeStatuses(t *testing.T) (*server, string) {
	t.Helper()
	s, _ := copyServer(t)
	account := serve(s, request(s, "GET", "/api/status", "")).Body.String()
	if w := serve(s, request(s, "POST", "/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.20"}}`)); w.Code != 200 {
		t.Fatalf("save lan = %d %s", w.Code, w.Body)
	}
	lan := serve(s, request(s, "GET", "/api/status", "")).Body.String()
	return s, `{account:` + account + `,lan:` + lan + `}`
}

func TestRouteChoicePage(t *testing.T) {
	_, statuses := routeStatuses(t)
	for _, scenario := range []string{"old file", "choose", "saved", "active", "copying"} {
		t.Run(scenario, func(t *testing.T) {
			runPageRegression(t, "tabs-route-test.js", "checkRouteChoice("+statuses+","+strconv.Quote(scenario)+")")
		})
	}
}

// The LAN form's Test and Save, with the server's own replies.
func TestLANFormPage(t *testing.T) {
	s, statuses := routeStatuses(t)
	replies := map[string]jsontext.Value{}
	for _, address := range []string{"192.0.2.20", "192.0.2.99", "not an address!"} {
		body, _ := json.Marshal(map[string]string{"address": address})
		w := serve(s, request(s, "POST", "/copy/api/test", string(body)))
		replies[address] = jsontext.Value(`{"status":` + strconv.Itoa(w.Code) + `,"body":` + w.Body.String() + `}`)
	}
	// The server Tests a Save itself, and refuses one that does not answer.
	w := serve(s, request(s, "POST", "/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.99"}}`))
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"lan_unreachable"`) {
		t.Fatalf("unreachable save = %d %s", w.Code, w.Body)
	}
	replies["unreachable"] = jsontext.Value(w.Body.String())
	locked, _ := json.Marshal(map[string]string{"code": "route_locked", "message": errorCopy("route_locked")})
	replies["locked"] = locked
	replies["scans"] = scanReplies(t, s)
	encoded, err := json.Marshal(replies)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"test passes", "test fails", "test bad address", "save", "save after test", "save unreachable", "save rejected", "scan lists", "test names", "saved route"} {
		t.Run(scenario, func(t *testing.T) {
			runPageRegression(t, "tabs-route-test.js", "checkLANForm("+statuses+","+string(encoded)+","+strconv.Quote(scenario)+")")
		})
	}
}

// scanReplies is the server's own /api/scan answers, by typed address or
// "" for a scan of the whole network: two telescopes, a blocked scan on a
// Mac, and silence.
func scanReplies(t *testing.T, s *server) jsontext.Value {
	t.Helper()
	saved := hostOS
	t.Cleanup(func() { hostOS = saved })
	hostOS = "darwin"
	answer := func(body string) string {
		w := serve(s, request(s, "POST", "/api/scan", body))
		return `{"status":` + strconv.Itoa(w.Code) + `,"body":` + w.Body.String() + `}`
	}
	useLAN(t, twoTelescopes())
	list := answer(`{}`)
	probe := answer(`{"address":"192.0.2.20"}`)
	useLAN(t, &lanNet{sendErr: &net.OpError{Op: "write", Net: "udp", Err: os.NewSyscallError("sendto", syscall.EHOSTUNREACH)}})
	blocked := answer(`{}`)
	useLAN(t, &lanNet{})
	silent := answer(`{}`)
	return jsontext.Value(`{"list":{"":` + list + `,"192.0.2.20":` + probe + `},"blocked":{"":` + blocked + `},"silent":{"":` + silent + `}}`)
}
