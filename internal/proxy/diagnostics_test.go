package proxy

import (
	"bytes"
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestDebugLayerTransitions(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	synctest.Test(t, func(t *testing.T) {
		var b bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: slog.New(slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug}))})
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		x := <-stacks
		x.post(func(now core.Instant) { x.drive(layerEvent{r: &rdt.Event{Kind: rdt.LocalAbort}}, now) })
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("runtime failure hidden")
		}
		c.Close()
		s.Close()
		for _, want := range []string{`"phase":"rendezvous"`, `"phase":"dtls"`, `"phase":"rdt"`, `"phase":"tunnel"`, `"state":"started"`, `"state":"ready"`, `"path":"direct"`, `"transport":"udp"`, `"version":`, `"suite":`, `"msg":"layer_failed","phase":"rdt","reason":"transport_failed"`} {
			if !strings.Contains(b.String(), want) {
				t.Errorf("missing %s in %s", want, b.String())
			}
		}
		for _, secret := range []string{"token-sentinel", "source-sentinel", "sim-user", "sim-pass", "sim-allocation", "EXAMPLEUID", "sim-license"} {
			if strings.Contains(b.String(), secret) {
				t.Fatal("secret in debug logs")
			}
		}
	})
}

type diagnosticListener struct{ net.Listener }

func (diagnosticListener) Accept() (net.Conn, error) {
	return nil, &net.OpError{Op: "secret-sentinel", Net: "tcp", Err: syscall.EMFILE}
}

func TestRuntimeListenerDiagnostic(t *testing.T) {
	l := listen(t)
	err := Serve(context.Background(), loopbackDialer{}, []Forward{{Listener: diagnosticListener{l}, Port: 4700}}, 1, nil)
	phase, reason := diagnostic.Fields(err, "session")
	if phase != "listener_accept" || reason != "descriptor_limit" {
		t.Fatalf("failure = %s/%s", phase, reason)
	}
	if strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatal("listener diagnostic leaked")
	}
}
