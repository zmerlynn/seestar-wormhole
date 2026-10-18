package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Every nonempty subset, every permutation, and both association orders cross
// normalization after every join. Elements model received, cancellation, queue
// EOF, own owed-release, attached constructor failure, and residual notification.
func TestTerminalCompositionTable(t *testing.T) {
	names := []string{"received", "cancellation", "queue-EOF", "owed-release", "constructor", "residual"}
	cases := 0
	var permute func([]int, []int)
	permute = func(order, remaining []int) {
		if len(remaining) != 0 {
			for i, item := range remaining {
				next := append([]int(nil), remaining[:i]...)
				next = append(next, remaining[i+1:]...)
				permute(append(order, item), next)
			}
			return
		}
		for _, right := range []bool{false, true} {
			for _, useJoin := range []bool{false, true} {
				for _, marker := range []error{nil, session.ErrBusy, session.ErrUncertain, session.ErrStop} {
					if len(order) == 1 && order[0] == 1 && marker != nil {
						continue
					}
					cases++
					leaves := []error{
						NewTerminal(session.RDT, "timeout", session.ErrTransport),
						NewTerminal(session.Cloud, "canceled", session.ErrTransport),
						NewTerminal(session.Rendezvous, "transport_failed", session.ErrTransport),
						NewTerminal(session.Cloud, "canceled", session.ErrReleaseAbandoned),
						Wrap("dtls", "capacity", session.ErrBudget),
						NewTerminal(session.Tunnel, "invalid_frame", session.ErrTransport),
					}
					project := func(err error) error {
						disposition := error(session.ErrTransport)
						if errors.Is(err, session.ErrReleaseAbandoned) {
							disposition = session.ErrReleaseAbandoned
						}
						return RetainTerminal(err, disposition)
					}
					join := func(a, b error) error {
						if useJoin {
							return JoinTerminals(a, b)
						}
						return project(errors.Join(a, b))
					}
					outcome := project(leaves[order[0]])
					if right {
						outcome = project(leaves[order[len(order)-1]])
						for i := len(order) - 2; i >= 0; i-- {
							outcome = join(leaves[order[i]], outcome)
						}
					} else {
						for _, i := range order[1:] {
							outcome = join(outcome, leaves[i])
						}
					}
					outcome = RetainTerminal(errors.Join(outcome, marker), session.ErrTransport)
					if marker != nil && !errors.Is(outcome, marker) {
						t.Fatalf("order=%v lost policy %v", order, marker)
					}
					var logs bytes.Buffer
					logger := slog.New(slog.NewJSONHandler(&logs, nil))
					LogFinal(logger, "failed", "session", errors.Join(context.Canceled, outcome), nil)
					before := logs.Len()
					// Repeat the normalized aggregate and each original input at the final
					// boundary: only already-owned receipts may suppress those echoes.
					LogFinal(logger, "failed", "session", outcome, nil)
					for _, i := range order {
						LogFinal(logger, "failed", "session", leaves[i], nil)
					}
					if logs.Len() != before {
						t.Fatalf("order=%v right=%v join=%v a received cause was missing before final echoes: %s", order, right, useJoin, &logs)
					}
					got := map[string]int{}
					decoder := jsontext.NewDecoder(&logs)
					for decoder.PeekKind() != 0 {
						var record struct{ Phase, Reason string }
						if err := json.UnmarshalDecode(decoder, &record, json.MatchCaseInsensitiveNames(true)); err != nil {
							t.Fatal(err)
						}
						got[record.Phase+"/"+record.Reason]++
					}
					want := map[string]int{}
					labels := []string{"rdt/timeout", "", "rendezvous/transport_failed", "cloud/release_abandoned", "dtls/capacity", "tunnel/invalid_frame"}
					owed := false
					primary := "cloud/canceled"
					for _, i := range order {
						if i != 1 {
							primary = labels[i]
						}
						if i != 1 && i != 3 {
							break
						}
					}
					if p, r := Fields(outcome, ""); p+"/"+r != primary {
						t.Fatalf("order=%v right=%v join=%v primary=%s/%s want %s", order, right, useJoin, p, r, primary)
					}
					for _, i := range order {
						if labels[i] != "" {
							want[labels[i]]++
						}
						owed = owed || i == 3
					}
					if errors.Is(outcome, session.ErrReleaseAbandoned) != owed {
						t.Fatalf("order=%v right=%v lost owed release", order, right)
					}
					if len(got) != len(want) {
						t.Fatalf("names=%v order=%v right=%v reports=%v want %v", names, order, right, got, want)
					}
					for label, n := range want {
						if got[label] != n {
							t.Fatalf("order=%v right=%v reports=%v want %v", order, right, got, want)
						}
					}
				}
			}
		}
	}
	for mask := 1; mask < 1<<len(names); mask++ {
		var subset []int
		for i := range names {
			if mask&(1<<i) != 0 {
				subset = append(subset, i)
			}
		}
		permute(nil, subset)
	}
	if cases != 31284 {
		t.Fatalf("table cases=%d want 31284", cases)
	}
}

// Pair every policy with every other policy in both join orders. One typed
// timeout and one obligated cancellation must each keep their own receipt,
// including when the timeout itself carries a weaker owed-release disposition.
func TestTerminalDispositionTable(t *testing.T) {
	policies := []error{session.ErrTransport, session.ErrReleaseAbandoned, session.ErrBusy,
		errors.Join(session.ErrBusy, session.ErrReleaseAbandoned), session.ErrUncertain,
		errors.Join(session.ErrUncertain, session.ErrReleaseAbandoned), session.ErrStop,
		errors.Join(session.ErrStop, session.ErrReleaseAbandoned)}
	for i, a := range policies {
		for j, b := range policies {
			for _, reversed := range []bool{false, true} {
				received := NewTerminal(session.RDT, "timeout", a)
				canceled := NewTerminal(session.Cloud, "canceled", b)
				first, second := error(received), error(canceled)
				if reversed {
					first, second = second, first
				}
				outcome := JoinTerminals(first, second)
				wantPolicy := error(session.ErrTransport)
				for _, candidate := range []error{session.ErrStop, session.ErrUncertain, session.ErrBusy} {
					if errors.Is(a, candidate) || errors.Is(b, candidate) {
						wantPolicy = candidate
						break
					}
				}
				if !errors.Is(outcome, wantPolicy) {
					t.Fatalf("policies=%d/%d reversed=%v lost %v", i, j, reversed, wantPolicy)
				}
				owed := errors.Is(a, session.ErrReleaseAbandoned) || errors.Is(b, session.ErrReleaseAbandoned)
				if errors.Is(outcome, session.ErrReleaseAbandoned) != owed {
					t.Fatalf("policies=%d/%d reversed=%v lost release marker", i, j, reversed)
				}
				if p, r := Fields(outcome, ""); p != "rdt" || r != "timeout" {
					t.Fatalf("policies=%d/%d reversed=%v primary=%s/%s", i, j, reversed, p, r)
				}
				var logs bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&logs, nil))
				Log(logger, "failed", "session", outcome)
				before := logs.Len()
				LogFinal(logger, "failed", "session", received, nil)
				LogFinal(logger, "failed", "session", canceled, nil)
				if logs.Len() != before {
					t.Fatalf("policies=%d/%d reversed=%v duplicate echo: %s", i, j, reversed, &logs)
				}
				records := 0
				decoder := jsontext.NewDecoder(&logs)
				for decoder.PeekKind() != 0 {
					var record map[string]any
					if err := json.UnmarshalDecode(decoder, &record, json.MatchCaseInsensitiveNames(true)); err != nil {
						t.Fatal(err)
					}
					records++
				}
				want := 2
				if b == session.ErrTransport {
					want = 1
				}
				if records != want {
					t.Fatalf("policies=%d/%d reversed=%v reports=%d want %d", i, j, reversed, records, want)
				}
			}
		}
	}
}

func TestAttachedReceiptUsesActualFallbackLayer(t *testing.T) {
	attached := Wrap("release", "timeout", session.ErrTimeout)
	normalized := AtLayer(attached, session.RDT, session.ErrTransport)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	Log(logger, "failed", "session", normalized)
	before := logs.Len()
	LogFinal(logger, "failed", "session", attached, nil)
	if logs.Len() != before {
		t.Fatalf("attached receipt duplicated: %s", &logs)
	}
	if p, r := Fields(normalized, ""); p != "rdt" || r != "timeout" {
		t.Fatalf("fallback attribution=%s/%s want rdt/timeout", p, r)
	}
}

func TestCancellationPolicyTranslationSharesReceipt(t *testing.T) {
	for _, policy := range []error{session.ErrReleaseAbandoned, session.ErrBusy, session.ErrUncertain, session.ErrStop} {
		received := NewTerminal(session.Cloud, "canceled", session.ErrTransport)
		normalized := RetainTerminal(received, policy)
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		Log(logger, "failed", "session", normalized)
		before := logs.Len()
		LogFinal(logger, "failed", "session", normalized, nil)
		LogFinal(logger, "failed", "session", received, nil)
		if before == 0 || logs.Len() != before {
			t.Fatalf("policy=%v missing or duplicated receipt: %s", policy, &logs)
		}
	}
}

func TestInformativeReceiptPrioritySurvivesProjection(t *testing.T) {
	policies := []error{session.ErrTransport, session.ErrReleaseAbandoned, session.ErrBusy,
		errors.Join(session.ErrBusy, session.ErrReleaseAbandoned), session.ErrUncertain,
		errors.Join(session.ErrUncertain, session.ErrReleaseAbandoned), session.ErrStop,
		errors.Join(session.ErrStop, session.ErrReleaseAbandoned)}
	for i, a := range policies {
		for j, b := range policies {
			for _, reversed := range []bool{false, true} {
				for _, early := range []bool{false, true} {
					first := NewTerminal(session.RDT, "timeout", a)
					second := NewTerminal(session.DTLS, "timeout", b)
					left, right := error(first), error(second)
					if reversed {
						left, right = right, left
					}
					if early {
						left = RetainTerminal(left, session.ErrStop)
					}
					outcome := RetainTerminal(JoinTerminals(left, right), session.ErrStop)
					want := "rdt"
					if j/2 > i/2 || (j/2 == i/2 && reversed) {
						want = "dtls"
					}
					if phase, reason := Fields(outcome, ""); phase != want || reason != "timeout" {
						t.Fatalf("policies=%d/%d reversed=%v early=%v primary=%s/%s want %s/timeout", i, j, reversed, early, phase, reason, want)
					}
					if !errors.Is(outcome, session.ErrStop) {
						t.Fatal("projected blocking disposition lost")
					}
					owed := errors.Is(a, session.ErrReleaseAbandoned) || errors.Is(b, session.ErrReleaseAbandoned)
					if errors.Is(outcome, session.ErrReleaseAbandoned) != owed {
						t.Fatal("owed release lost")
					}
					var logs bytes.Buffer
					logger := slog.New(slog.NewJSONHandler(&logs, nil))
					Log(logger, "failed", "session", outcome)
					before := logs.Len()
					LogFinal(logger, "failed", "session", first, nil)
					LogFinal(logger, "failed", "session", second, nil)
					if logs.Len() != before {
						t.Fatalf("duplicated informative receipt: %s", &logs)
					}
					n := 0
					decoder := jsontext.NewDecoder(&logs)
					for decoder.PeekKind() != 0 {
						var record map[string]any
						if err := json.UnmarshalDecode(decoder, &record, json.MatchCaseInsensitiveNames(true)); err != nil {
							t.Fatal(err)
						}
						n++
					}
					if n != 2 {
						t.Fatalf("informative reports=%d want 2", n)
					}
				}
			}
		}
	}
}

func TestAttachedOperationComposition(t *testing.T) {
	typed := NewTerminal(session.RDT, "timeout", session.ErrTransport)
	attached := Wrap("release", "release_failed", session.ErrStop)
	raw := errors.Join(typed, attached)
	normalized := RetainTerminal(raw, session.ErrStop)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	Log(logger, "failed", "session", normalized)
	LogFinal(logger, "failed", "session", typed, nil)
	LogFinal(logger, "failed", "session", attached, nil)
	if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 2 {
		t.Fatalf("reports=%d want 2: %s", n, &logs)
	}
}
func TestAttachedBlockingCancellation(t *testing.T) {
	for _, policy := range []error{session.ErrStop, session.ErrUncertain, session.ErrBusy, session.ErrReleaseAbandoned} {
		attached := Wrap("cloud", "canceled", policy)
		normalized := RetainTerminal(attached, policy)
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		Log(logger, "failed", "session", normalized)
		LogFinal(logger, "failed", "session", attached, nil)
		if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 1 {
			t.Fatalf("reports=%d want 1: %s", n, &logs)
		}
	}
}
