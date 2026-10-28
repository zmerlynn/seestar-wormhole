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
