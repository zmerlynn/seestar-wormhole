//go:build unix || windows

package proxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

// Count completed non-consuming socket checks, including the production
// getsockopt call, so FIN preservation is tested with the watcher running.
type admissionCountRaw struct {
	syscall.RawConn
	checked chan struct{}
}

func (r admissionCountRaw) Control(f func(uintptr)) error {
	err := r.RawConn.Control(f)
	select {
	case r.checked <- struct{}{}:
	default:
	}
	return err
}

type admissionFINConn struct {
	*resetReadCount
	checked chan struct{}
}

func (c admissionFINConn) SyscallConn() (syscall.RawConn, error) {
	raw, err := c.resetReadCount.SyscallConn()
	return admissionCountRaw{raw, c.checked}, err
}

func TestAdmissionRealTCPSaturatedFINPreservesBytes(t *testing.T) {
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := net.DialTCP("tcp", nil, l.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	local, err := l.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counted := admissionFINConn{&resetReadCount{local, make(chan int, 8)}, make(chan struct{}, 1)}
	d := admissionGateDial{make(chan admissionDialRequest)}
	single := &e2eSingleListener{conn: counted, done: make(chan struct{})}
	finished := make(chan error, 1)
	go func() { finished <- Serve(ctx, d, []Forward{{Listener: single, Port: 445}}, 1, nil) }()
	defer func() {
		cancel()
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	req := <-d.requests
	want := bytes.Repeat([]byte{0, 255, 37, 19}, 6<<10)
	if _, err := client.Write(want); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-counted.reads:
		case <-time.After(5 * time.Second):
			t.Fatal("preread did not saturate")
		}
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	// Drain a possibly earlier check, then wait for a fresh kernel check.
	select {
	case <-counted.checked:
	default:
	}
	select {
	case <-counted.checked:
	case <-time.After(5 * time.Second):
		t.Fatal("no socket check after FIN")
	}
	if err := req.ctx.Err(); err != nil {
		t.Fatalf("FIN canceled admission: %v", err)
	}
	remote, device := net.Pipe()
	defer device.Close()
	device.SetDeadline(time.Now().Add(5 * time.Second))
	end := make(chan struct{})
	req.result <- &admissionHalfConn{remote, end}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(device, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("queued TCP bytes changed across FIN handoff")
	}
	select {
	case <-end:
	case <-time.After(5 * time.Second):
		t.Fatal("queued bytes were not followed by FIN")
	}
	written := make(chan error, 1)
	go func() { _, err := device.Write([]byte("reply")); device.Close(); written <- err }()
	reply, err := io.ReadAll(client)
	if err != nil || string(reply) != "reply" {
		t.Fatalf("reverse after FIN = %q, %v", reply, err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}
