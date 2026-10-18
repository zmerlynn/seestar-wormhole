package proxy

import (
	"bytes"
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestBusyLifecycleWithoutClients(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs logBuffer
		s := simulatedSession(t, context.Background(), Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, &simulatedCloud{busy: true})
		defer s.Close()
		synctest.Wait()
		data := string(logs.take())
		if !strings.Contains(data, `"reason":"busy"`) || !strings.Contains(data, `"phase":"backoff"`) || !strings.Contains(data, `"local_clients":"accept_close"`) {
			t.Fatalf("missing retryable busy lifecycle: %s", data)
		}
		<-time.After(time.Minute)
		synctest.Wait()
		if !strings.Contains(string(logs.take()), `"phase":"backoff"`) {
			t.Fatal("busy did not continue retrying")
		}
	})
}

func TestLifecycleReleaseBackoffAndIdle(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		var b bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: slog.New(slog.NewJSONHandler(&b, nil)), Idle: time.Second})
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		x := <-stacks
		x.post(func(now core.Instant) {
			x.drive(layerEvent{i: &iotc.Event{Kind: iotc.AttemptFailed, Attempt: x.id}}, now)
		})
		c.Read(make([]byte, 1))
		c.Close()
		synctest.Wait()
		time.Sleep(2 * time.Second)
		c, err = s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		s.Close()
		for _, want := range []string{`"phase":"dialing"`, `"phase":"ready"`, `"phase":"backoff"`, `"reason":"transport_failed"`, `"reason":"idle"`, `"msg":"session_release_complete"`, `"retry_ms":`} {
			if !strings.Contains(b.String(), want) {
				t.Fatalf("missing %s in %s", want, b.String())
			}
		}
		for _, secret := range []string{"sim-user", "sim-pass", "sim-allocation", "EXAMPLEUID"} {
			if strings.Contains(b.String(), secret) {
				t.Fatal("secret in lifecycle log")
			}
		}
	})
}
