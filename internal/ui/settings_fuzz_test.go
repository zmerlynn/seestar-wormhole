package ui

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzSettings(f *testing.F) {
	for _, seed := range []string{
		`{"version":1}`, `{"version":1,"ports":[{"local":0,"device":445}]}`,
		`{"version":1,"future":{"enabled":true},"ports":[{"local":1445,"device":445,"future_flag":true}]}`,
		`{"version":1,"ports":[{"local":1.5,"device":445}]}`,
		`{"version":1,"ports":[{"local":65536,"device":445}]}`,
		`{"version":2}`, `{}`, `null`, `{`, `{"version":1,"idle":"1h30m"}`,
		`{"version":1}` + strings.Repeat(" ", maxJSON-len(`{"version":1}`)),
		`{"version":1}` + strings.Repeat(" ", maxJSON+1-len(`{"version":1}`)),
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > maxJSON+1 {
			return
		}
		st := testStorage(t)
		path := filepath.Join(st.root, "config.json")
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		s, err := loadSettings(st)
		if err != nil {
			if !st.settingsRefused {
				t.Fatal("refusal did not latch")
			}
			if err := st.write("config.json", defaultSettings()); err == nil {
				t.Fatal("refused file was overwritten")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(b, after) {
				t.Fatal("refused file changed")
			}
			return
		}
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal("accepted settings cannot be encoded")
		}
		if len(encoded) > maxJSON {
			return
		} // Normalization can grow an input near the limit.
		if err := st.write("config.json", s); err != nil {
			t.Fatal(err)
		}
		round, err := loadSettings(st)
		if err != nil {
			t.Fatal("accepted settings cannot survive a save")
		}
		again, err := json.Marshal(round)
		if err != nil || !bytes.Equal(encoded, again) {
			t.Fatal("save lost settings or unknown members")
		}
	})
}
