package iotcedge

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

// Child isolation keeps the injected budget and diagnostic sink out of other
// tests, including repeated and race-enabled package runs.
func ridBudgetChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv("WORMHOLE_RID_BUDGET_CHILD") == t.Name() {
		return true
	}
	for _, format := range []string{"json", "text"} {
		cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
		cmd.Env = append(os.Environ(), "WORMHOLE_RID_BUDGET_CHILD="+t.Name(), "WORMHOLE_RID_FORMAT="+format)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("RID budget child (%s): %v\n%s", format, err, out)
		}
	}
	return false
}

func TestRIDHistoryBudgetWorkerFailure(t *testing.T) {
	if !ridBudgetChild(t) {
		return
	}
	processRIDs.limit = 1
	var first, replacement IOTCCrypto
	generate := iotc.Action{Kind: iotc.Generate, Attempt: 1, Token: 7}
	e, err := first.Execute(generate)
	if err != nil || e.Kind != iotc.MaterialReady {
		t.Fatalf("first generation: kind=%v err=%v", e.Kind, err)
	}
	first.Clear()
	for _, worker := range []*IOTCCrypto{&replacement, &first} {
		e, err := worker.Execute(generate)
		var exhausted *RIDHistoryExhaustedError
		if !errors.As(err, &exhausted) || !errors.Is(err, ErrIOTCCrypto) {
			t.Fatalf("replacement error=%v, want typed RID exhaustion", err)
		}
		if err.Error() != ridExhaustionMessage || e.Material != (iotc.Material{}) || e.Kind == iotc.MaterialReady {
			t.Fatalf("exhaustion returned material or wrong diagnostic: kind=%v err=%v", e.Kind, err)
		}
		if worker.private != nil || len(worker.keys) != 0 {
			t.Fatal("failed generation installed cryptographic state")
		}
		worker.Clear()
	}
}

func TestRIDHistoryBudgetSetupFailure(t *testing.T) {
	if !ridBudgetChild(t) {
		return
	}
	processRIDs.limit = 1
	processRIDs.used = map[[8]byte]struct{}{{1}: {}}
	var logs bytes.Buffer
	logger := diagnostic.Logger(os.Getenv("WORMHOLE_RID_FORMAT"), &logs, slog.LevelInfo).With("owner", "budget-test")
	m, err := iotc.New(iotc.Config{Attempt: 1, RecordSID: 7,
		UID: [20]byte([]byte("EXAMPLEUID0000000000")), License: iotc.License{Realm: "test"}, LocalNAT: 2})
	if err != nil {
		t.Fatal(err)
	}
	var generate iotc.Action
	for _, a := range m.Step(iotc.Event{Kind: iotc.Start, Attempt: 1}, 0) {
		if a.Kind == iotc.Generate {
			generate = a
		}
	}
	if generate.Token == 0 {
		t.Fatal("setup did not request generation")
	}
	actions := make(chan iotc.Action, 1)
	actions <- generate
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var completion iotc.Event
	// Execute only Generate: no DNS, peer connection or packet send. Returning
	// false joins the ordinary executor's children and clears its crypto worker.
	(&IOTCIO{Logger: logger}).Run(ctx, actions, func(e iotc.Event) bool { completion = e; return false })
	if completion.Kind != iotc.EffectFailed || completion.Token != generate.Token || completion.Attempt != generate.Attempt || completion.Material != (iotc.Material{}) {
		t.Fatalf("exhaustion completion kind=%v token=%d, want correlated EffectFailed", completion.Kind, completion.Token)
	}
	failed, disposed := 0, 0
	for _, a := range m.Step(completion, 0) {
		switch a.Kind {
		case iotc.Failed:
			failed++
		case iotc.Disposed:
			disposed++
		case iotc.Send, iotc.OpenTCP, iotc.Ready:
			t.Fatalf("failed setup emitted action %v", a.Kind)
		}
	}
	if m.State() != iotc.Closed || failed != 1 || disposed != 1 {
		t.Fatalf("setup state=%v failed=%d disposed=%d, want Closed/1/1", m.State(), failed, disposed)
	}
	if os.Getenv("WORMHOLE_RID_FORMAT") == "text" {
		for _, want := range []string{"level=ERROR", "msg=iotc_rid_history_exhausted", `error="` + ridExhaustionMessage + `"`, "owner=budget-test"} {
			if !strings.Contains(logs.String(), want) {
				t.Fatalf("missing %q in diagnostic: %s", want, &logs)
			}
		}
		return
	}
	var diagnostic struct{ Level, Msg, Error, Owner string }
	if err := json.Unmarshal(logs.Bytes(), &diagnostic, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("missing exhaustion diagnostic: %v", err)
	}
	if diagnostic.Owner != "budget-test" || diagnostic.Level != "ERROR" || diagnostic.Msg != "iotc_rid_history_exhausted" || diagnostic.Error != ridExhaustionMessage {
		t.Fatalf("diagnostic=%+v, want fixed exhaustion error and restart guidance", diagnostic)
	}
}

func TestRIDHistoryBudgetNoLogger(t *testing.T) {
	if !ridBudgetChild(t) {
		return
	}
	processRIDs.limit = 1
	processRIDs.used = map[[8]byte]struct{}{{1}: {}}
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	actions := make(chan iotc.Action, 1)
	link := iotc.Link{ID: 41}
	actions <- iotc.Action{Kind: iotc.Generate, Attempt: 9, Token: 17, Link: link}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	(&IOTCIO{}).Run(ctx, actions, func(e iotc.Event) bool {
		if e.Kind != iotc.EffectFailed || e.Attempt != 9 || e.Token != 17 || e.Link != link {
			t.Errorf("completion=%+v, want correlated failure", e)
		}
		return false
	})
	if logs.Len() != 0 {
		t.Fatal("executor used process-default logger without an owned logger")
	}
}
