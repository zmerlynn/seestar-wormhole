package ui

import (
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewNestedFuturePortMember(t *testing.T) {
	s := testServer(t)
	fixture := []byte(`{"version":1,"bind":"127.0.0.1","idle":"0","ports":[{"local":1445,"device":445,"future_flag":true}]}`)
	if err := s.storage.writeBytes("config.json", fixture); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSettings(s.storage)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.saveSettings(loaded); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.storage.root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"future_flag":true`) {
		t.Fatal("unknown nested port member lost during rewrite")
	}
}

// Page submissions contain only known numeric fields and may reorder rows.
func TestPagePortEditsKeepUnknownMembersByDevice(t *testing.T) {
	s := testServer(t)
	fixture := []byte(`{"version":1,"bind":"127.0.0.1","idle":"0","ports":[{"local":1445,"device":445,"future_flag":{"keep":true}},{"local":0,"device":80,"future_http":[1,2]}]}`)
	if err := s.storage.writeBytes("config.json", fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(s.storage); err != nil {
		t.Fatal(err)
	}
	draft := testSettings()
	draft.Ports = []proxyrun.Port{{Local: 8080, Device: 80}, {Local: 2445, Device: 445}}
	if err := s.saveSettings(draft); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(s.storage)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ports[0].Local != 8080 || !strings.Contains(string(got.Ports[0].Unknown), `"future_http":[1,2]`) || got.Ports[1].Local != 2445 || !strings.Contains(string(got.Ports[1].Unknown), `"future_flag":{"keep":true}`) {
		t.Fatal("known edits lost or future fields moved between services")
	}
	effective, _, err := got.validate()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range effective {
		if len(p.Unknown) != 0 {
			t.Fatal("storage metadata leaked into effective mappings")
		}
	}
}
