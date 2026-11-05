package ui

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strconv"
	"testing"
)

func TestTabsPage(t *testing.T) {
	runPageRegression(t, "tabs-route-test.js", "checkTabs()")
}

func TestActivityHoldsDetails(t *testing.T) {
	runPageRegression(t, "tabs-route-test.js", "checkActivityMove()")
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
	w := serve(s, request(s, "POST", "/api/route", `{"settings":{"route":"lan","lan_address":"not an address!"}}`))
	if w.Code != 400 {
		t.Fatalf("bad save = %d", w.Code)
	}
	replies["badSave"] = jsontext.Value(w.Body.String())
	locked, _ := json.Marshal(map[string]string{"code": "route_locked", "message": errorCopy("route_locked")})
	replies["locked"] = locked
	encoded, err := json.Marshal(replies)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"test passes", "test fails", "test bad address", "save", "save after test", "save rejected"} {
		t.Run(scenario, func(t *testing.T) {
			runPageRegression(t, "tabs-route-test.js", "checkLANForm("+statuses+","+string(encoded)+","+strconv.Quote(scenario)+")")
		})
	}
}
