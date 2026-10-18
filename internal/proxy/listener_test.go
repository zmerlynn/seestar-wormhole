package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
)

type loopbackDialer struct{ address string }

func (d loopbackDialer) DialPort(ctx context.Context, _ uint16) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp4", d.address)
}
func listen(t *testing.T) net.Listener {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func dial(t *testing.T, l net.Listener) *net.TCPConn {
	t.Helper()
	c, e := net.DialTimeout("tcp4", l.Addr().String(), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c.(*net.TCPConn)
}

func TestListenerHalfCloseBothDirections(t *testing.T) {
	for _, deviceFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(deviceFirst), func(t *testing.T) {
			device, local := listen(t), listen(t)
			defer device.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- Serve(ctx, loopbackDialer{device.Addr().String()}, []Forward{{Listener: local, Port: 4700}}, 8, nil)
			}()
			client := dial(t, local)
			defer client.Close()
			remote, e := device.Accept()
			if e != nil {
				t.Fatal(e)
			}
			defer remote.Close()
			remote.SetDeadline(time.Now().Add(5 * time.Second))
			request, response := []byte(`{"method":"get_setting"}`+"\r\n"), []byte("response")
			if deviceFirst {
				remote.Write(response)
				remote.(*net.TCPConn).CloseWrite()
				b, e := io.ReadAll(client)
				if e != nil || string(b) != "response" {
					t.Fatalf("peer END %q %v", b, e)
				}
				client.Write(request)
				client.CloseWrite()
				b, e = io.ReadAll(remote)
				if e != nil || !bytes.Equal(b, request) {
					t.Fatalf("reverse direction %q %v", b, e)
				}
			} else {
				client.Write(request)
				client.CloseWrite()
				b, e := io.ReadAll(remote)
				if e != nil || !bytes.Equal(b, request) {
					t.Fatalf("local END %q %v", b, e)
				}
				remote.Write(response)
				remote.(*net.TCPConn).CloseWrite()
				b, e = io.ReadAll(client)
				if e != nil || string(b) != "response" {
					t.Fatalf("response %q %v", b, e)
				}
			}
			cancel()
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				t.Fatal("shutdown blocked")
			}
		})
	}
}
func TestListenerSlowClientManyClientsAndShutdown(t *testing.T) {
	device, local := listen(t), listen(t)
	defer device.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, loopbackDialer{device.Addr().String()}, []Forward{{Listener: local, Port: 445}}, 64, nil)
	}()
	var peers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, e := device.Accept()
			if e != nil {
				return
			}
			peers.Go(func() { defer c.Close(); c.SetDeadline(time.Now().Add(5 * time.Second)); io.Copy(c, c) })
		}
	}()
	slow := dial(t, local)
	slow.SetReadBuffer(1024)
	defer slow.Close()
	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		b := make([]byte, 8192)
		for {
			if _, e := slow.Write(b); e != nil {
				return
			}
		}
	}()
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Go(func() {
			c := dial(t, local)
			defer c.Close()
			c.Write([]byte("ok"))
			c.CloseWrite()
			b, e := io.ReadAll(c)
			if e != nil || string(b) != "ok" {
				t.Errorf("sibling %q %v", b, e)
			}
		})
	}
	workers.Wait()
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("slow client blocked shutdown")
	}
	slow.Close()
	<-blocked
	device.Close()
	<-acceptDone
	peers.Wait()
}
func TestListenerStartStopDoesNotLeak(t *testing.T) {
	for i := 0; i < 20; i++ {
		l := listen(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		started := make(chan struct{})
		go pprof.Do(ctx, pprof.Labels("listener_test", t.Name()), func(ctx context.Context) {
			close(started)
			done <- Serve(ctx, loopbackDialer{"127.0.0.1:1"}, []Forward{{Listener: l, Port: 80}}, 2, nil)
		})
		<-started
		c := dial(t, l)
		if i == 0 && listenerWorkerCount(t, t.Name()) == 0 {
			t.Fatal("live Serve worker missing from owned inventory")
		}
		cancel()
		c.Close()
		<-done
	}
	// WaitGroup completion and the buffered result can precede goroutine exit.
	// Require every labeled descendant to exit, without counting unrelated
	// workers or relying on NumGoroutine's concurrent free-list accounting.
	if after := waitListenerWorkers(t, t.Name(), time.Second); after != 0 {
		var stacks bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("listener workers remaining after shutdown deadline: %d\n%s", after, stacks.String())
	}
}

// pprof labels propagate to goroutines started by Serve, including accept,
// client, bridge and dial workers. Its goroutine profile enumerates live stacks;
// the process-wide NumGoroutine estimate can include concurrent free-list moves.
func listenerWorkerCount(t *testing.T, name string) int {
	t.Helper()
	return ownedWorkerCount(t, "listener_test", name)
}

func waitListenerWorkers(t *testing.T, name string, bound time.Duration) int {
	t.Helper()
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		n := listenerWorkerCount(t, name)
		if n == 0 {
			return 0
		}
		select {
		case <-deadline.C:
			return n
		case <-tick.C:
		}
	}
}

func TestListenerWorkerInventory(t *testing.T) {
	// These workers model unrelated tests or library teardown. They must not
	// change the listener's worker inventory, but the original global count does.
	var others sync.WaitGroup
	stopOthers := make(chan struct{})
	for i := 0; i < 27; i++ {
		others.Go(func() { <-stopOthers })
	}
	defer func() { close(stopOthers); others.Wait() }()
	ready, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// Return from Do before sampling so only the child retains this label.
	pprof.Do(context.Background(), pprof.Labels("listener_test", t.Name()), func(context.Context) {
		go func() {
			defer close(done)
			close(ready)
			<-stop
		}()
	})
	<-ready
	var release sync.Once
	finish := func() { release.Do(func() { close(stop); <-done }) }
	defer finish()
	if n := listenerWorkerCount(t, t.Name()); n != 1 {
		t.Fatalf("own live workers=%d; want 1 with 27 unrelated workers", n)
	}
	if n := waitListenerWorkers(t, t.Name(), 10*time.Millisecond); n != 1 {
		t.Fatalf("blocked owned worker escaped deadline check: got %d; want 1", n)
	}
	finish()
	if n := waitListenerWorkers(t, t.Name(), time.Second); n != 0 {
		t.Fatalf("completed owned workers=%d; want 0", n)
	}
}
