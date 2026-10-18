package proxy

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"os"
	"testing"
)

func siteFixture(t testing.TB) [][]byte {
	t.Helper()
	b, err := os.ReadFile("testdata/site-4700.txt")
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Split(bytes.TrimSuffix(b, []byte("\r\n")), []byte("\r\n"))
}

func TestSiteFixtureLines(t *testing.T) {
	for _, fixture := range siteFixture(t) {
		line := append(bytes.Clone(fixture), '\r', '\n')
		got, event := protectSite(line)
		var request struct {
			Method string
			ID     jsontext.Value
		}
		if err := json.Unmarshal(line, &request, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		switch request.Method {
		case "set_user_location", "pi_set_time":
			if len(got) != 0 || event == nil || event.reason != "site_protected" {
				t.Fatalf("fixture setter forwarded: %s", got)
			}
			var reply struct {
				ID    jsontext.Value
				Error siteRPCError
			}
			if json.Unmarshal(event.rejection, &reply, json.MatchCaseInsensitiveNames(true)) != nil || !bytes.Equal(reply.ID, request.ID) || reply.Error.Code != -32602 {
				t.Fatalf("reply=%s", event.rejection)
			}
		default:
			if !bytes.Equal(got, line) || event != nil {
				t.Fatalf("fixture changed: %s", got)
			}
		}
	}
}
