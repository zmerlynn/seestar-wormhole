package iotcedge

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
)

// Exercise the real Run constructor on consecutive attempts, not a manually
// populated IOTCCrypto. This catches omission of Run's KeyLog propagation.
func TestRunKeylogFreshAttempts(t *testing.T) {
	path := t.TempDir() + "/keys"
	keys, err := keylog.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	var previous [8]byte
	var previousKey [16]byte
	var previousIV [12]byte
	for _, attempt := range []uint64{7, 8} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		actions := make(chan iotc.Action, 1)
		events := make(chan iotc.Event, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			(&IOTCIO{KeyLog: keys}).Run(ctx, actions, func(e iotc.Event) bool {
				select {
				case events <- e:
					return true
				case <-ctx.Done():
					return false
				}
			})
		}()
		next := func(a iotc.Action) iotc.Event {
			t.Helper()
			actions <- a
			select {
			case e := <-events:
				return e
			case <-ctx.Done():
				t.Fatal("effect stalled")
				return iotc.Event{}
			}
		}
		e := next(iotc.Action{Kind: iotc.Generate, Attempt: attempt})
		if e.Kind != iotc.MaterialReady || e.Material.RID == previous || e.Material.MasterKey == previousKey || e.Material.MasterIV == previousIV {
			t.Fatal("missing/fixed attempt material")
		}
		previous = e.Material.RID
		previousKey, previousIV = e.Material.MasterKey, e.Material.MasterIV
		d := next(iotc.Action{Kind: iotc.Derive, Attempt: attempt, Private: 1, Public: e.Material.Public})
		if d.Kind != iotc.Derived {
			t.Fatal("derive failed")
		}
		cancel()
		<-done
	}
	keys.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"# IOTC_KEY 7 ", "# IOTC_KEY 8 "} {
		if strings.Count(string(b), prefix) != 2 {
			t.Fatal("production Run lost attempt key records")
		}
	}
}
