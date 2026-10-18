package proxy

import (
	"context"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type trackingFactory struct {
	*factory
	stacks chan *stack
}

func (f *trackingFactory) New(id uint64, b session.Budgets) (sessionedge.Layers, error) {
	l, e := f.factory.New(id, b)
	if e == nil {
		f.stacks <- l.Rendezvous.(*stack)
	}
	return l, e
}
func TestCloseReturnsAfterPionWorkers(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		for i := 0; i < 20; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			client, err := newFixtureClient(t, cloudhttp.Config{Transport: &simulatedCloud{}})
			if err != nil {
				t.Fatal(err)
			}
			stacks := make(chan *stack, 1)
			var trace []string
			s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
				return &trackingFactory{&factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}, stacks}
			})
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatal(err)
			}
			x := <-stacks
			// Read only the pointer identity to distinguish production Pion from the test peer.
			ptr := reflect.ValueOf(x.secure).Elem().FieldByName("conn").Pointer()
			c.Close()
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			cancel()
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			needle := fmt.Sprintf("github.com/pion/dtls/v3.(*Conn).readAndBuffer(0x%x", ptr)
			if strings.Contains(string(buf[:n]), needle) {
				t.Fatalf("production Pion reader still live after Session.Close on cycle %d:\n%s", i, buf[:n])
			}
			synctest.Wait()
			client.Close()
		}
	})
}

func assertDTLSWorkersExited(t *testing.T, x *stack) {
	t.Helper()
	ptr := reflect.ValueOf(x.secure).Elem().FieldByName("conn").Pointer()
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	for _, worker := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(worker, "github.com/pion/dtls/v3.") && strings.Contains(worker, fmt.Sprintf("0x%x", ptr)) {
			t.Fatalf("DTLS worker survived shutdown: %s", worker)
		}
	}
}

func assertStackWorkersExited(t *testing.T, x *stack) {
	t.Helper()
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	for _, worker := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(worker, "github.com/zmerlynn/seestar-wormhole/internal/proxy.(*stack).") && strings.Contains(worker, fmt.Sprintf("%p", x)) {
			t.Fatalf("composition worker survived shutdown: %s", worker)
		}
	}
}
