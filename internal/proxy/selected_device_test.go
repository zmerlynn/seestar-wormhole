package proxy

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestSelectedDeviceStatusAndReady(t *testing.T) {
	for _, tc := range []struct{ serial, model, label string }{
		{"serial-sentinel-secret-1234", "S50 Pro", "S50 Pro serial-sentinel-secret-1234"},
		{"serial-sentinel-secret-1234", "Seestar S50 Pro", "Seestar S50 Pro serial-sentinel-secret-1234"},
		{"tiny", "S30", "S30 tiny"},
		{strings.Repeat("s", 64), "S30", "S30 " + strings.Repeat("s", 64)},
		{strings.Repeat("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", 2), "S30", "S30 " + strings.Repeat("!\\\"#$%&'()*+,-./:;<=>?@[\\\\]^_`{|}~", 2)},
		{"serial-sentinel-secret-1234", "private-model", "private-model serial-sentinel-secret-1234"},
	} {
		t.Run(tc.model+"/"+fmt.Sprint(len(tc.serial)), func(t *testing.T) {
			var logs bytes.Buffer
			encodedSerial, err := json.Marshal(tc.serial)
			if err != nil {
				t.Fatal(err)
			}
			peer := &pairAllocationCloud{t: t, list: `{"data":{"total":1,"items":[{"deviceSn":` + string(encodedSerial) + `,"deviceModel":"` + tc.model + `","deviceStatus":1,"ssid":"synthetic"}]}}`}
			client, err := newFixtureClient(t, cloudhttp.Config{Transport: peer})
			if err != nil {
				t.Fatal(err)
			}
			status := NewStatus()
			s, err := newWithClient(t.Context(), Config{Idle: time.Minute, Status: status, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, client)
			if err != nil {
				t.Fatal(err)
			}
			// Join the lazy owner before replaying a ready observation: no dial,
			// live socket, allocation or concurrent owner writes are involved.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if status.SelectedDevice() != tc.label {
				t.Fatalf("selected=%q want=%q", status.SelectedDevice(), tc.label)
			}
			logs.Reset()
			s.Observe(session.Event{Kind: session.Progress, Layer: session.Tunnel}, session.Snapshot{State: session.Dialing}, session.Snapshot{State: session.Ready}, 1)
			var record struct {
				Phase     string `json:"phase"`
				Telescope string `json:"telescope"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatal(err)
			}
			t.Logf("ready record bytes=%d", logs.Len())
			if record.Phase != "ready" || record.Telescope != tc.label {
				t.Fatalf("ready=%s", &logs)
			}
			if peer.allocs.Load() != 0 || peer.details.Load() != 0 {
				t.Fatal("lazy selection allocated or queried detail")
			}
		})
	}
}
