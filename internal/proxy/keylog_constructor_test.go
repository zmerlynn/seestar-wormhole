package proxy

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/edge/iotcedge"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
)

// Exercise the default constructor shared with New. Do not install KeyLog on
// IOTCIO or IOTCCrypto here: that would bypass the handoff this test protects.
func TestDefaultWireKeylogFreshAttempts(t *testing.T) {
	path := t.TempDir() + "/keys"
	keys, err := keylog.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	wire := defaultWireFactory(Config{KeyLog: keys})
	type capture struct {
		attempt  uint64
		material iotc.Material
		derived  iotc.Event
		sealed   []byte
	}
	var captures []capture
	var retired iotc.KeyRef
	for _, attempt := range []uint64{7, 8} {
		func() {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			actions := make(chan iotc.Action)
			events := make(chan iotc.Event)
			done := make(chan struct{})
			runner := wire(iotc.License{})
			go func() {
				defer close(done)
				runner.Run(ctx, actions, func(e iotc.Event) bool {
					select {
					case events <- e:
						return true
					case <-ctx.Done():
						return false
					}
				})
			}()
			defer func() { cancel(); <-done }()
			next := func(a iotc.Action, want iotc.EventKind) iotc.Event {
				t.Helper()
				a.Attempt = attempt
				a.Token = 91
				select {
				case actions <- a:
				case <-ctx.Done():
					t.Fatal("default wire did not accept effect")
				}
				select {
				case e := <-events:
					if e.Kind != want || e.Attempt != attempt || e.Token != a.Token {
						t.Fatalf("completion kind/attempt/token = %v/%d/%d, want %v/%d/%d", e.Kind, e.Attempt, e.Token, want, attempt, a.Token)
					}
					return e
				case <-ctx.Done():
					t.Fatal("default wire effect stalled")
					return iotc.Event{}
				}
			}
			// A handle from the joined Run must not authorize crypto in its successor.
			if retired != 0 {
				next(iotc.Action{Kind: iotc.Seal, Mode: iotc.AESGCM, Key: retired, Data: []byte("retired")}, iotc.EffectFailed)
			}
			m := next(iotc.Action{Kind: iotc.Generate}, iotc.MaterialReady).Material
			d := next(iotc.Action{Kind: iotc.Derive, Private: m.PrivateRef, Public: m.Public}, iotc.Derived)
			s := next(iotc.Action{Kind: iotc.Seal, Mode: iotc.AESGCM, Key: d.Key, Data: []byte("synthetic constructor payload"), AAD: []byte("synthetic envelope")}, iotc.Encrypted)
			captures = append(captures, capture{attempt, m, d, s.Data})
			retired = d.Key
		}()
	}
	if captures[0].material.RID == captures[1].material.RID || captures[0].material.MasterKey == captures[1].material.MasterKey || captures[0].material.MasterIV == captures[1].material.MasterIV {
		t.Fatal("default wire reused attempt material")
	}
	if err := keys.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 {
		t.Fatalf("default wire saved %d key records, want 4 (Generate and Derive per attempt)", bytes.Count(b, []byte("# IOTC_KEY ")))
	}
	for i, c := range captures {
		want := fmt.Sprintf("# IOTC_KEY %d %x %d %x %x", c.attempt, c.material.RID, c.material.MasterRef, c.material.MasterKey, c.material.MasterIV)
		if lines[2*i] != want {
			t.Fatalf("attempt %d: saved master record differs from generated material", c.attempt)
		}
		var attempt, ref uint64
		var rid, key, iv []byte
		if _, err := fmt.Sscanf(lines[2*i+1], "# IOTC_KEY %d %x %d %x %x", &attempt, &rid, &ref, &key, &iv); err != nil {
			t.Fatal("invalid derived record", err)
		}
		if attempt != c.attempt || ref != uint64(c.derived.Key) || !bytes.Equal(rid, c.material.RID[:]) || len(key) != 16 || !bytes.Equal(iv, c.derived.IV[:]) {
			t.Fatalf("attempt %d: derived record identity or extent differs", c.attempt)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := gcm.Open(nil, iv, c.sealed, []byte("synthetic envelope"))
		if err != nil || string(plain) != "synthetic constructor payload" {
			t.Fatalf("attempt %d: saved pair cannot authenticate executor output: %v", c.attempt, err)
		}
	}
}

func TestDefaultWireLogger(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		var logs bytes.Buffer
		logger := diagnostic.Logger(format, &logs, slog.LevelDebug).With("owner", "session")
		runner := defaultWireFactory(Config{Logger: logger})(iotc.License{}).(*iotcedge.IOTCIO)
		if runner.Logger != logger {
			t.Fatal("default wire factory lost session logger")
		}
	}
	if runner := defaultWireFactory(Config{})(iotc.License{}).(*iotcedge.IOTCIO); runner.Logger != nil {
		t.Fatal("default wire factory substituted a global logger")
	}
}
