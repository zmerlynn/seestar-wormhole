package iotcedge

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestIOTCIOUDPAndCancellation(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.SetDeadline(time.Now().Add(30 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actions := make(chan iotc.Action, 4)
	events := make(chan iotc.Event, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&IOTCIO{}).Run(ctx, actions, func(e iotc.Event) bool {
			select {
			case events <- e:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	actions <- iotc.Action{Kind: iotc.Send, Link: iotc.Link{Address: peer.LocalAddr().(*net.UDPAddr).AddrPort()}, Data: []byte("request")}
	b := make([]byte, 64)
	n, addr, err := peer.ReadFromUDPAddrPort(b)
	if err != nil || string(b[:n]) != "request" {
		t.Fatalf("send: %q %v", b[:n], err)
	}
	peer.WriteToUDPAddrPort([]byte("reply"), addr)
	select {
	case e := <-events:
		if e.Kind != iotc.Wire || string(e.Data) != "reply" || e.Marker.Seconds == 0 {
			t.Fatal("wrong ingress")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no ingress")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("workers did not join")
	}
}
func TestIOTCIOTCPFraming(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actions := make(chan iotc.Action, 4)
	events := make(chan iotc.Event, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&IOTCIO{}).Run(ctx, actions, func(e iotc.Event) bool {
			select {
			case events <- e:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	link := iotc.Link{Transport: iotc.TCP, Address: netip.MustParseAddrPort(l.Addr().String()), ID: 4}
	actions <- iotc.Action{Kind: iotc.OpenTCP, Link: link}
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	packet, err := iotc.EncodeLegacy(iotc.Packet{Opcode: 0x8004, Route: 0x3f, Body: make([]byte, 24)}, iotc.ScrambleKey([16]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	c.Write(packet[:3])
	c.Write(packet[3:])
	got := false
	for !got {
		select {
		case e := <-events:
			if e.Kind == iotc.Wire {
				if string(e.Data) != string(packet) || e.Link != link {
					t.Fatal("framing/generation changed")
				}
				got = true
			}
		case <-time.After(30 * time.Second):
			t.Fatal("missing framed packet")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("TCP workers did not join")
	}
}
