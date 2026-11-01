package ui

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSerialBoundSettings(t *testing.T) {
	for _, tc := range []struct {
		serial string
		valid  bool
	}{
		{strings.Repeat("s", 63), true}, {strings.Repeat("s", 64), true}, {strings.Repeat("s", 65), false},
		{strings.Repeat("\u202e", 11), false}, {"s ", false}, {"s\x7f", false}, {"s\u0080", false},
	} {
		t.Run(strconv.QuoteToASCII(tc.serial), func(t *testing.T) {
			settings := testSettings()
			settings.Serial = tc.serial
			_, _, err := settings.validate()
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || actionError(err).Code != "serial" || actionError(err).Field != "serial" {
				t.Fatalf("manual validation=%v", err)
			}
			st := testStorage(t)
			body, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.writeBytes("config.json", body); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(st.root, "config.json")
			before, _ := os.Stat(path)
			loaded, err := loadSettings(st)
			if tc.valid {
				if err != nil || loaded.Serial != tc.serial {
					t.Fatalf("saved validation=%v", err)
				}
			} else {
				if err == nil || actionError(err).Code != "serial" || !st.settingsRefused {
					t.Fatalf("saved refusal=%v refused=%v", err, st.settingsRefused)
				}
				if err := st.writeBytes("config.json", []byte(`{"version":1}`)); err == nil {
					t.Fatal("refused settings overwritten")
				}
				// Manual submission fails before credentials, persistence or a run.
				c := testController(nil)
				if err := c.start(settings, testPassword); err == nil || actionError(err).Code != "serial" {
					t.Fatalf("manual start=%v", err)
				}
			}
			after, err := os.ReadFile(path)
			info, _ := os.Stat(path)
			if err != nil || !bytes.Equal(after, body) || !os.SameFile(before, info) || !before.ModTime().Equal(info.ModTime()) {
				t.Fatal("load rewrote settings")
			}
		})
	}
	html, _ := assets.ReadFile("web/index.html")
	if !strings.Contains(string(html), `id="serial" placeholder="From your Seestar account" type="text" maxlength="64"`) {
		t.Fatal("manual input bound missing")
	}
}
