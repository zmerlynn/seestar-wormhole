package proxy

import (
	"context"
	"testing"
	"time"
)

func TestSiteIncompleteFrameShutdown(t *testing.T) {
	device, local := listen(t), listen(t)
	defer device.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, loopbackDialer{device.Addr().String()}, []Forward{{Listener: local, Port: 4700}}, 1, nil)
	}()
	client := dial(t, local)
	defer client.Close()
	remote, err := device.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	if _, err := client.Write([]byte(`{"method":"pi_set_time"`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial-frame worker blocked shutdown")
	}
}
