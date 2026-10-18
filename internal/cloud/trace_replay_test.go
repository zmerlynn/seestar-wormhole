package cloud_test

import (
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord/tracetest"
	"testing"
)

func TestRecordedAccountRenewalReplay(t *testing.T) {
	credentials, err := cloud.EmailCredentials("fixture@example.invalid", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	token, err := cloud.ParseLogin(200, []byte(`{"code":200,"data":{"accessToken":"synthetic-token"}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	capture := tracetest.New(t)
	m := cloud.NewAccountMachine(credentials)
	scheduler := sim.New[cloud.AccountEvent](0, 32)
	scheduler.Schedule(0, cloud.AccountEvent{Kind: cloud.AccountBegin}, 0)
	var inputs []cloud.AccountEvent
	renewed := false
	err = scheduler.Run(100, 32, func(e cloud.AccountEvent, now core.Instant) error {
		a := m.Step(e, now)
		inputs = append(inputs, e)
		capture.Step("account", 1, e, []cloud.AccountAction{a}, now)
		switch a.Kind {
		case cloud.AccountSendLogin:
			scheduler.Schedule(now+1, cloud.AccountEvent{Kind: cloud.AccountLoginDone, Sequence: a.Sequence, Token: token}, 0)
		case cloud.AccountSendProfile:
			var failure error
			if !renewed {
				failure = cloud.ErrUnauthorized
				renewed = true
			}
			scheduler.Schedule(now+1, cloud.AccountEvent{Kind: cloud.AccountProfileDone, Sequence: a.Sequence, Err: failure}, 0)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e := cloud.AccountEvent{Kind: cloud.AccountLogout}
	inputs = append(inputs, e)
	capture.Step("account", 1, e, []cloud.AccountAction{m.Step(e, 101)}, 101)
	rr := capture.Fixture("testdata/trace-account.jsonl")
	fresh := cloud.NewAccountMachine(credentials)
	index := 0
	hydrate := func(r tracerecord.Record) (cloud.AccountEvent, error) {
		e := inputs[index]
		index++
		e.Kind = cloud.AccountEventKind(r.Input.Kind)
		e.Sequence = r.Input.Token
		return e, nil
	}
	step := func(e cloud.AccountEvent, now core.Instant) []cloud.AccountAction {
		return []cloud.AccountAction{fresh.Step(e, now)}
	}
	if err := tracerecord.Replay(rr, "account", 1, hydrate, step); err != nil {
		t.Fatal(err)
	}
}
