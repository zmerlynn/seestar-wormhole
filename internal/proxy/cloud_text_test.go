package proxy

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// A cloud failure keeps its adapter error's text through normalization,
// joins and the policy primary to the final record, with its phase,
// reason, policy and receipt unchanged.
func TestComposedCloudFailureText(t *testing.T) {
	const text = "dial tcp 192.0.2.10:443: peer closed during request"
	terminal := cloudTerminal(fmt.Errorf("%s: %w", text, cloud.ErrTransport)).(*diagnostic.Terminal)
	joined := diagnostic.JoinTerminals(diagnostic.RetainTerminal(terminal, session.ErrTransport), context.Canceled)
	primary := diagnostic.Primary(joined).(*diagnostic.Terminal)
	if !primary.SharesReceipt(terminal) || !errors.Is(primary, session.ErrTransport) || primary.Permanent {
		t.Fatalf("receipt or policy changed: %v", primary)
	}
	for _, err := range []error{terminal, joined, primary} {
		var logs bytes.Buffer
		diagnostic.LogOutcome(slog.New(slog.NewJSONHandler(&logs, nil)), "proxy failed", "session", err, nil)
		var record struct {
			Phase  string `json:"phase"`
			Reason string `json:"reason"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Phase != "cloud" || record.Reason != "transport_failed" || record.Error != "cloud: transport_failed: "+text+": "+cloud.ErrTransport.Error() {
			t.Errorf("final record %+v", record)
		}
	}
}
