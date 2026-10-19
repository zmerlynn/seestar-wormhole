package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"log/slog"
	"reflect"
	"testing"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func diagnosticRecordCounts(t *testing.T, b *bytes.Buffer) map[string]int {
	t.Helper()
	got := map[string]int{}
	d := jsontext.NewDecoder(bytes.NewReader(b.Bytes()))
	for d.PeekKind() != 0 {
		var r struct{ Phase, Reason string }
		if err := json.UnmarshalDecode(d, &r, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		got[r.Phase+"/"+r.Reason]++
	}
	return got
}

// Cross layer and origin independently; receipt consumption can occur before,
// between or after two normalizations, independently of join order.

func TestAttachmentReceiptOrderMatrix(t *testing.T) {
	rows := 0
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		for origin := session.Cloud; origin <= StackLayer; origin++ {
			for form := 0; form < 3; form++ {
				for reverse := 0; reverse < 2; reverse++ {
					for receiptOrder := 0; receiptOrder < 4; receiptOrder++ {
						for projection := 0; projection < 3; projection++ {
							rows++
							name := fmt.Sprintf("layer%d_origin%d_form%d_reverse%d_receipts%d_projection%d", layer, origin, form, reverse, receiptOrder, projection)
							t.Run(name, func(t *testing.T) {
								var a, b error = NewTerminal(origin, "timeout", session.ErrTransport), NewTerminal((origin+1)%6, "capacity", session.ErrReleaseAbandoned)
								pa := NewTerminal(origin, "timeout", nil).Phase()
								pb := NewTerminal((origin+1)%6, "capacity", nil).Phase()
								if form == 1 {
									a = Wrap(pa, "timeout", session.ErrTimeout)
									b = Wrap(pb, "capacity", session.ErrReleaseAbandoned)
								}
								if form == 2 {
									a = fmt.Errorf("credential=SECRET_UID payload=PRIVATE_BYTES: %w", a)
									b = fmt.Errorf("coords=PRIVATE_COORDS time=PRIVATE_TIME: %w", b)
								}
								var logs bytes.Buffer
								l := slog.New(slog.NewJSONHandler(&logs, nil))
								if receiptOrder == 1 {
									Log(l, "first", "session", a)
								}
								if receiptOrder == 2 {
									LogFinal(l, "first", "session", b, nil)
								}
								raw := errors.Join(a, context.Canceled, b)
								if reverse == 1 {
									raw = errors.Join(b, context.Canceled, a)
								}
								out := AtLayer(raw, layer, session.ErrTransport)
								if receiptOrder == 3 {
									LogFinal(l, "first", "session", out, nil)
								}
								switch projection {
								case 0:
									out = RetainTerminal(out, session.ErrStop)
								case 1:
									out = JoinTerminals(NewTerminal(session.Cloud, "canceled", session.ErrTransport), out)
								case 2:
									out = AtLayer(out, (layer+1)%session.LayerCount, session.ErrBusy)
								}
								Log(l, "owner", "session", out)
								before := logs.Len()
								for _, echo := range []error{a, b, raw, out} {
									LogFinal(l, "final", "session", echo, nil)
								}
								if logs.Len() != before {
									t.Errorf("new reports on echo: %s", &logs)
								}
								want := map[string]int{pa + "/timeout": 1, pb + "/capacity": 1}
								if got := diagnosticRecordCounts(t, &logs); !reflect.DeepEqual(got, want) {
									t.Errorf("reports=%v want=%v", got, want)
								}
								if !errors.Is(out, session.ErrReleaseAbandoned) {
									t.Error("lost release obligation")
								}
							})
						}
					}
				}
			}
		}
	}
	t.Logf("rows=%d", rows)
}

func TestWrappedIndependentAttachments(t *testing.T) {
	for origin := session.Cloud; origin <= StackLayer; origin++ {
		for preReported := 0; preReported < 3; preReported++ {
			t.Run(fmt.Sprintf("origin%d_pre%d", origin, preReported), func(t *testing.T) {
				phase := NewTerminal(origin, "timeout", nil).Phase()
				a := Wrap(phase, "timeout", session.ErrTimeout)
				b := Wrap(phase, "capacity", session.ErrBudget)
				raw := Wrap("stack", "handshake_failed", errors.Join(a, context.Canceled, b))
				var logs bytes.Buffer
				l := slog.New(slog.NewJSONHandler(&logs, nil))
				if preReported == 1 {
					Log(l, "early", "session", a)
				}
				if preReported == 2 {
					Log(l, "early", "session", b)
				}
				out := AtLayer(raw, session.DTLS, session.ErrTransport)
				Log(l, "owner", "session", out)
				got := diagnosticRecordCounts(t, &logs)
				want := map[string]int{phase + "/timeout": 1, phase + "/capacity": 1}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("reports before final echoes=%v want=%v; logs=%s", got, want, &logs)
				}
				before := logs.Len()
				LogFinal(l, "echo", "session", a, nil)
				LogFinal(l, "echo", "session", b, nil)
				if logs.Len() != before {
					t.Errorf("independent receipts not consumed: %s", &logs)
				}
			})
		}
	}
}

func TestQuietAttachmentEcho(t *testing.T) {
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		for _, policy := range []error{session.ErrReleaseAbandoned, session.ErrBusy, session.ErrUncertain, session.ErrStop} {
			t.Run(fmt.Sprintf("layer%d_%v", layer, policy), func(t *testing.T) {
				phase := NewTerminal(layer, "timeout", nil).Phase()
				raw := Wrap(phase, "canceled", context.Canceled)
				first := AtLayer(raw, layer, policy)
				second := AtLayer(raw, layer, policy)
				var logs bytes.Buffer
				l := slog.New(slog.NewJSONHandler(&logs, nil))
				Log(l, "owner", "session", first)
				before := logs.Len()
				LogFinal(l, "echo", "session", second, nil)
				if before == 0 || logs.Len() != before {
					t.Errorf("one translated attachment duplicated: %s", &logs)
				}
			})
		}
	}
}

func TestTypedAttachmentWrapper(t *testing.T) {
	a := NewTerminal(session.RDT, "timeout", session.ErrTransport)
	b := NewTerminal(session.Tunnel, "capacity", session.ErrTransport)
	raw := Wrap("stack", "handshake_failed", errors.Join(a, context.Canceled, b))
	var logs bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&logs, nil))
	Log(l, "owner", "session", AtLayer(raw, session.DTLS, session.ErrTransport))
	want := map[string]int{"rdt/timeout": 1, "tunnel/capacity": 1}
	if got := diagnosticRecordCounts(t, &logs); !reflect.DeepEqual(got, want) {
		t.Errorf("reports=%v want=%v", got, want)
	}
	before := logs.Len()
	LogFinal(l, "echo", "session", raw, nil)
	if logs.Len() != before {
		t.Errorf("wrapper echo: %s", &logs)
	}
}
