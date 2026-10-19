package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Cross synchronous graph and receipt dimensions independently. Runtime queue
// publication, shutdown and lease ownership are covered by the public Run tests.
func TestTerminalGraphCompositionTable(t *testing.T) {
	// All five ordered binary parenthesizations of four inputs, not just the
	// left/right association extremes in TestTerminalCompositionTable.
	shapes := []func([]error, func(error, error) error) error{
		func(v []error, j func(error, error) error) error { return j(j(j(v[0], v[1]), v[2]), v[3]) },
		func(v []error, j func(error, error) error) error { return j(j(v[0], j(v[1], v[2])), v[3]) },
		func(v []error, j func(error, error) error) error { return j(j(v[0], v[1]), j(v[2], v[3])) },
		func(v []error, j func(error, error) error) error { return j(v[0], j(j(v[1], v[2]), v[3])) },
		func(v []error, j func(error, error) error) error { return j(v[0], j(v[1], j(v[2], v[3]))) },
	}
	rows := 0
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		for origin := session.Cloud; origin <= StackLayer; origin++ {
			for form := 0; form < 4; form++ {
				for shape, associate := range shapes {
					for wrapper := 0; wrapper < 4; wrapper++ {
						for repeat := 0; repeat < 3; repeat++ {
							for reverse := 0; reverse < 2; reverse++ {
								for receiptOrder := 0; receiptOrder < 4; receiptOrder++ {
									for normalizeJoins := 0; normalizeJoins < 2; normalizeJoins++ {
										rows++
										// Bits independently choose Terminal/attachment for each receipt.
										pa := NewTerminal(origin, "timeout", nil).Phase()
										pb := NewTerminal((origin+1)%6, "capacity", nil).Phase()
										var a, b error = NewTerminal(origin, "timeout", session.ErrTransport), NewTerminal((origin+1)%6, "capacity", session.ErrReleaseAbandoned)
										if form&1 != 0 {
											a = Wrap(pa, "timeout", session.ErrTimeout)
										}
										if form&2 != 0 {
											b = Wrap(pb, "capacity", errors.Join(session.ErrBudget, session.ErrReleaseAbandoned))
										}
										var logs bytes.Buffer
										logger := slog.New(slog.NewJSONHandler(&logs, nil))
										if receiptOrder == 1 {
											Log(logger, "early", "session", a)
										}
										if receiptOrder == 2 {
											LogFinal(logger, "early", "session", b, nil)
										}
										// Equal labels with a new identity are independent; the
										// other modes repeat the original raw/normalized receipt.
										duplicate := error(NewTerminal(origin, "timeout", session.ErrTransport))
										if repeat == 1 {
											duplicate = a
										}
										if repeat == 2 {
											duplicate = AtLayer(a, layer, session.ErrTransport)
										}
										leaves := []error{a, b, context.Canceled, duplicate}
										primary := pa + "/timeout"
										if reverse == 1 {
											leaves[0], leaves[1] = leaves[1], leaves[0]
											primary = pb + "/capacity"
										}
										join := func(a, b error) error {
											joined := errors.Join(a, b)
											switch wrapper {
											case 1:
												joined = fmt.Errorf("PRIVATE_COORDS SECRET_UID: %w", joined)
											case 2:
												joined = Wrap("stack", "handshake_failed", joined)
											case 3:
												joined = Wrap("release", "release_failed", Wrap("dtls", "handshake_failed", joined))
											}
											if normalizeJoins == 1 {
												return RetainTerminal(joined, session.ErrTransport)
											}
											return joined
										}
										raw := associate(leaves, join)
										first := AtLayer(raw, layer, session.ErrTransport)
										if receiptOrder == 3 {
											LogFinal(logger, "between", "session", a, nil)
										}
										// Normalize the original graph again as well as projecting the first
										// result. Each route must share all original receipt identities.
										second := AtLayer(raw, (layer+1)%session.LayerCount, session.ErrBusy)
										out := RetainTerminal(first, session.ErrStop)
										name := fmt.Sprintf("layer=%d origin=%d form=%d shape=%d wrapper=%d repeat=%d reverse=%d receipts=%d joins=%d", layer, origin, form, shape, wrapper, repeat, reverse, receiptOrder, normalizeJoins)
										for _, value := range []error{raw, first, second, out} {
											p, r := Fields(value, "session")
											if p+"/"+r != primary {
												t.Fatalf("%s primary=%s/%s want %s", name, p, r, primary)
											}
										}
										if !errors.Is(out, session.ErrStop) || !errors.Is(out, session.ErrReleaseAbandoned) || !errors.Is(second, session.ErrBusy) {
											t.Fatalf("%s lost policy or release obligation", name)
										}
										Log(logger, "owner", "session", out)
										want := map[string]int{pa + "/timeout": 1, pb + "/capacity": 1}
										if repeat == 0 {
											want[pa+"/timeout"]++
										}
										if got := diagnosticRecordCounts(t, &logs); !reflect.DeepEqual(got, want) {
											t.Fatalf("%s reports before echoes=%v want %v", name, got, want)
										}
										before := logs.Len()
										for _, echo := range []error{a, b, duplicate, raw, first, second, out} {
											LogFinal(logger, "echo", "session", echo, nil)
										}
										if logs.Len() != before {
											t.Fatalf("%s duplicated receipt: %s", name, &logs)
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if rows != 115200 {
		t.Fatalf("rows=%d want 115200", rows)
	}
}

// A quiet attachment contributes no report until its own normalization gains a
// policy. An informative sibling must never consume or lend it a receipt.
func TestQuietAttachmentRepeatedNormalization(t *testing.T) {
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		for _, policy := range []error{session.ErrReleaseAbandoned, session.ErrBusy, session.ErrUncertain, session.ErrStop} {
			quiet := Wrap(NewTerminal(layer, "canceled", nil).Phase(), "canceled", context.Canceled)
			other := Wrap("rdt", "timeout", session.ErrTimeout)
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			Log(logger, "quiet", "session", quiet)
			if logs.Len() != 0 {
				t.Fatalf("quiet cancellation reported: %s", &logs)
			}
			joined := AtLayer(errors.Join(quiet, other), layer, session.ErrTransport)
			Log(logger, "sibling", "session", joined)
			first := AtLayer(quiet, layer, policy)
			LogFinal(logger, "translation", "session", first, nil)
			before := logs.Len()
			second := AtLayer(quiet, layer, policy)
			third := RetainTerminal(second, policy)
			for _, echo := range []error{quiet, other, joined, first, second, third} {
				LogFinal(logger, "echo", "session", echo, nil)
			}
			if logs.Len() != before {
				t.Fatalf("layer=%d policy=%v duplicate: %s", layer, policy, &logs)
			}
			records := diagnosticRecordCounts(t, &logs)
			n := 0
			for _, count := range records {
				n += count
			}
			if n != 2 {
				t.Fatalf("layer=%d policy=%v reports=%v want two receipts", layer, policy, records)
			}
		}
	}
}

func TestContextualWrapperDirectReporting(t *testing.T) {
	for origin := session.Cloud; origin <= StackLayer; origin++ {
		for form := 0; form < 4; form++ {
			for final := 0; final < 2; final++ {
				pa := NewTerminal(origin, "timeout", nil).Phase()
				pb := NewTerminal((origin+1)%6, "capacity", nil).Phase()
				var a, b error = NewTerminal(origin, "timeout", session.ErrTransport), NewTerminal((origin+1)%6, "capacity", session.ErrTransport)
				if form&1 != 0 {
					a = Wrap(pa, "timeout", session.ErrTimeout)
				}
				if form&2 != 0 {
					b = Wrap(pb, "capacity", session.ErrBudget)
				}
				raw := Wrap("release", "release_failed", Wrap("stack", "handshake_failed", errors.Join(a, context.Canceled, b)))
				var logs bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&logs, nil))
				if final == 0 {
					Log(logger, "owner", "session", raw)
				} else {
					LogFinal(logger, "final", "session", raw, nil)
				}
				want := map[string]int{pa + "/timeout": 1, pb + "/capacity": 1}
				if got := diagnosticRecordCounts(t, &logs); !reflect.DeepEqual(got, want) {
					t.Fatalf("origin=%d form=%d final=%d reports=%v want %v", origin, form, final, got, want)
				}
				before := logs.Len()
				for _, echo := range []error{a, b, raw, AtLayer(raw, session.DTLS, session.ErrTransport)} {
					LogFinal(logger, "echo", "session", echo, nil)
				}
				if logs.Len() != before {
					t.Fatalf("origin=%d form=%d final=%d duplicated receipt: %s", origin, form, final, &logs)
				}
			}
		}
	}
}
