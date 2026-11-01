//go:build gui && guitest

package ui

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestGUITestRunner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newGUITestRunner()
		ctx, cancel := context.WithCancel(t.Context())
		status := proxy.NewStatus()
		done := make(chan error, 1)
		go func() {
			done <- runObserved(cancel, status.Rejected(), func() error { return f.run(ctx, Settings{}, cloud.Credentials{}, status) })
		}()
		for _, step := range []struct {
			event session.Event
			want  string
		}{
			{session.Event{Kind: session.Progress}, "Ready"},
			{session.Event{Kind: session.Failed, Err: session.ErrBusy}, "Busy — waiting for the other session"},
			{session.Event{Kind: session.Failed, Err: session.ErrTransport}, "Reconnecting"},
			{session.Event{Kind: session.Progress}, "Ready"},
			{session.Event{Kind: session.Failed, Err: diagnostic.NewTerminal(session.Cloud, "unauthorized", session.ErrUncertain)}, "Sign-in required — correct credentials and Start again"},
		} {
			f.steps <- step.event
			synctest.Wait()
			if got := status.Text(); got != step.want {
				t.Fatalf("%q, want %q", got, step.want)
			}
		}
		<-f.releasing
		select {
		case <-done:
			t.Fatal("release returned before join")
		default:
		}
		time.Sleep(7 * time.Second)
		select {
		case <-done:
			t.Fatal("slow release returned early")
		default:
		}
		time.Sleep(time.Second)
		<-f.released
		err := <-done
		if !errors.Is(ctx.Err(), context.Canceled) || finalStatus(err) != errorCopy("signin") {
			t.Fatal(err)
		}
	})
}

func TestGUITestConnectingCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newGUITestRunner()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- f.run(ctx, Settings{}, cloud.Credentials{}, proxy.NewStatus()) }()
		cancel()
		<-f.releasing
		select {
		case <-done:
			t.Fatal("cancellation bypassed release")
		default:
		}
		time.Sleep(8 * time.Second)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

func TestGUITestFailsClosed(t *testing.T) {
	t.Setenv("SEESTAR_GUITEST_OUT", "")
	t.Setenv("SEESTAR_GUITEST_SCENARIO", "")
	if err := runPlatformUI(t.Context(), t.TempDir(), nil, nil); err == nil {
		t.Fatal("fixture ran without harness configuration")
	}
}

func TestGUITestRemoveTimeoutRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		chain := &guiTestKeychain{root: t.TempDir(), scenario: "remove-timeout"}
		email := testSettings().Email
		if err := chain.Set(keyringService, email, testPassword); err != nil {
			t.Fatal(err)
		}
		c := &credentialStore{chain: chain, savedEmail: email}
		if err := c.forgetAt(t.Context(), email, c.nextGeneration()); actionError(err).Code != "keychain_timeout" {
			t.Fatal(err)
		}
		if state := c.state(email); !state.Busy || !state.Saved {
			t.Fatalf("late native removal was not exposed: %+v", state)
		}
		// A dispatched retry queues behind the first native call and treats
		// its late deletion as success. No GUI or real keychain is involved.
		if err := c.forgetAt(t.Context(), email, c.nextGeneration()); err != nil {
			t.Fatal(err)
		}
		if state := c.state(email); state.Busy || state.Saved || state.Code != "password_removed" {
			t.Fatalf("retry did not confirm removal: %+v", state)
		}
		if chain.removes != 2 {
			t.Fatalf("native Delete calls = %d, want 2", chain.removes)
		}
	})
}

func TestEagerSettingsSaveOnlyOnFirstReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newGUITestRunner()
		c := testController(f.run)
		saves := 0
		c.save = func(s Settings) error {
			saves++
			if s.Email != testSettings().Email {
				t.Error("wrong run snapshot")
			}
			return nil
		}
		if err := c.start(testSettings(), testPassword); err != nil {
			t.Fatal(err)
		}
		<-f.entered
		synctest.Wait()
		if saves != 0 {
			t.Fatal("saved on acknowledgment/armed before Ready")
		}
		for range 2 {
			f.steps <- session.Event{Kind: session.Progress}
			synctest.Wait()
		}
		if saves != 1 {
			t.Fatalf("Ready saved %d times", saves)
		}
		c.quit()
	})
}

func TestGUITestTelescopeSelection(t *testing.T) {
	for _, scenario := range []string{"telescope-picker", "dark-telescope-picker"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newGUITestRunner()
				f.scenario = scenario
				c := testController(f.run)
				original := testSettings()
				if err := c.start(original, testPassword); err != nil {
					t.Fatal(err)
				}
				<-f.entered
				synctest.Wait()
				if view := c.view(); view.Active || view.Failure == nil || view.Failure.Code != "selection_multiple" || len(view.Failure.Devices) != 2 {
					t.Fatal("runner failed to expose catalog", view)
				}
				original.Serial = "synthetic-choice-5678"
				original.Model = "Future telescope\tmodel"
				if err := c.start(original, testPassword); err != nil {
					t.Fatal(err)
				}
				<-f.entered
				synctest.Wait()
				if view := c.view(); view.SelectedDevice != `Future telescope\tmodel ***5678` {
					t.Fatal("runner failed to publish selected label", view)
				}
				f.steps <- session.Event{Kind: session.Progress}
				synctest.Wait()
				if err := c.changeTelescope(); err != nil {
					t.Fatal(err)
				}
				if view := c.view(); view.Active || view.SelectedDevice != "" {
					t.Fatal("runner survived Change telescope", view)
				}
				<-f.releasing
				<-f.released
			})
		})
	}
}
