package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Synthetic values and literal bytes model the sanitized run9/run16/run17
// REPORT.md observations, not requirements on the device's HTTP/RTSP services.
const observedHTTPGet = "GET / HTTP/1.1\r\nHost: seestar\r\nConnection: close\r\n\r\n"
const observedHTTPHeader = "HTTP/1.1 200 OK\r\nAccept-Ranges: bytes\r\nConnection: close\r\nContent-Length: 131072\r\nContent-Type: application/octet-stream\r\nETag: /boot/Image/1310721791423199\r\n\r\n"
const observedUpdaterGreeting = "{\"Event\":\"Version\",\"Timestamp\":\"1234.123456789\",\"name\":\"ASI AIR updater\"}\r\n"

// This peer scripts local socket completions; tunnel/RDT/DTLS/IOTC remain real.
// Its maps are confined to the peer owner. Inspect only after synctest.Wait.
type observedApplicationPeer struct {
	t       *testing.T
	ports   map[byte]uint16
	pending map[byte][]byte
	request []byte
	frames  map[uint16][][]byte
	opens   map[uint16]int
	writes  map[uint16]int
}

func (p *observedApplicationPeer) socket(a p2ptunnel.Action) ([]p2ptunnel.Event, bool) {
	ready := event(a, p2ptunnel.ReadReady)
	switch a.Kind {
	case p2ptunnel.Send:
		if len(a.Data) >= 4 && a.Data[0] >= 2 && a.Data[0] <= 4 {
			port := p.ports[a.Data[1]]
			b := bytes.Clone(a.Data)
			b[1] = 0 // Channel is allocated dynamically; every other byte is literal.
			p.frames[port] = append(p.frames[port], b)
		}
	case p2ptunnel.ConnectLocal:
		p.ports[a.Channel] = a.Port
		p.opens[a.Port]++
		if a.Port == 4700 {
			return nil, false // Existing synthetic RPC peer keeps a sibling busy.
		}
		e := event(a, p2ptunnel.Connected)
		switch a.Port {
		case 4554, 4555:
			e.Failed = true // Connect failure, result 1.
		case 4350:
			// Observed updater greeting immediately on connect.
			p.pending[a.Channel] = []byte(observedUpdaterGreeting)
		}
		return []p2ptunnel.Event{e, ready}, true
	case p2ptunnel.WriteLocal:
		port := p.ports[a.Channel]
		p.writes[port] += len(a.Data)
		if port == 4700 {
			return nil, false
		}
		if port != 80 {
			p.t.Errorf("unexpected client write on port %d: %x", port, a.Data)
		}
		p.request = append(p.request, a.Data...)
		if !bytes.HasPrefix([]byte(observedHTTPGet), p.request) {
			p.t.Errorf("HTTP request=%q, want prefix of %q", p.request, observedHTTPGet)
		}
		if string(p.request) == observedHTTPGet {
			p.pending[a.Channel] = []byte(observedHTTPHeader)
		}
		e := event(a, p2ptunnel.LocalWritten)
		e.Count = len(a.Data)
		return []p2ptunnel.Event{e, ready}, true
	case p2ptunnel.ReadLocal:
		port := p.ports[a.Channel]
		if port == 4700 {
			return nil, false
		}
		e := event(a, p2ptunnel.LocalData)
		data := p.pending[a.Channel]
		if len(data) > 0 {
			e.Data = data
			delete(p.pending, a.Channel)
			return []p2ptunnel.Event{e, ready}, true
		}
		if port == 80 && string(p.request) == observedHTTPGet {
			e.Kind = p2ptunnel.LocalEOF // Drain DATA then END.
		}
		return []p2ptunnel.Event{e}, true
	}
	return nil, false
}

func TestObservedApplicationPeersThroughByteCopy(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		p := &observedApplicationPeer{t: t, ports: map[byte]uint16{}, pending: map[byte][]byte{}, frames: map[uint16][][]byte{}, opens: map[uint16]int{}, writes: map[uint16]int{}}
		// Bypass fixtureAccountTransport so simulatedCloud counts actual login
		// requests too; otherwise an unintended relogin would be invisible.
		cloud := &simulatedCloud{}
		client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: cloud})
		if err != nil {
			t.Fatal(err)
		}
		stacks := make(chan *stack, 16)
		s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
			return &workloadFactory{&factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
				trace := []string{}
				w := newSimulatedWire(t, &trace)
				w.socketScript = p.socket
				return w
			}}, stacks}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var clients []net.Conn
		var serving []<-chan error
		defer func() {
			cancel()
			for _, c := range clients {
				c.Close()
			}
			for _, done := range serving {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
		}()
		connect := func(port uint16) net.Conn {
			local, client := net.Pipe()
			client.SetDeadline(time.Now().Add(time.Minute))
			clients = append(clients, client)
			l := &e2eSingleListener{conn: local, done: make(chan struct{})}
			done := make(chan error, 1)
			serving = append(serving, done)
			go func() { done <- Serve(ctx, s, []Forward{{Listener: l, Port: port}}, 8, nil) }()
			return client
		}
		checkLease := func() {
			t.Helper()
			synctest.Wait()
			if cloud.allocs.Load() != 1 || cloud.logins.Load() != 1 || cloud.details.Load() != 1 || cloud.stops.Load() != 0 || len(stacks) != 0 {
				t.Fatalf("session changed: allocations/login/detail/stop/stacks=%d/%d/%d/%d/%d, want 1/1/1/0/0", cloud.allocs.Load(), cloud.logins.Load(), cloud.details.Load(), cloud.stops.Load(), len(stacks))
			}
		}
		rpc := connect(4700)
		rpcReader := bufio.NewReader(rpc)
		exchange := func() {
			t.Helper()
			if _, err := io.WriteString(rpc, "{\"id\":7,\"method\":\"get_device_state\"}\r\n"); err != nil {
				t.Fatal(err)
			}
			got, err := rpcReader.ReadString('\n')
			const want = "{\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"state\":\"idle\"}}\n"
			if err != nil || got != want {
				t.Fatalf("sibling RPC=%q, %v; want %q, nil", got, err, want)
			}
		}
		exchange()
		x := <-stacks
		checkLease()

		http := connect(80)
		if _, err := io.WriteString(http, observedHTTPGet); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(http)
		if err != nil || string(got) != observedHTTPHeader {
			t.Fatalf("raw header then EOF=%q, %v; want %q, nil", got, err, observedHTTPHeader)
		}
		var one [1]byte
		if n, err := http.Read(one[:]); n != 0 || err != io.EOF {
			t.Fatalf("HTTP terminal read=%d, %v; want 0, EOF", n, err)
		}
		exchange()
		checkLease()

		updater := connect(4350)
		greeting := make([]byte, len(observedUpdaterGreeting))
		if _, err := io.ReadFull(updater, greeting); err != nil || string(greeting) != observedUpdaterGreeting {
			t.Fatalf("passive updater=%q, %v; want %q, nil", greeting, err, observedUpdaterGreeting)
		}
		exchange()
		checkLease()
		if p.writes[4350] != 0 {
			t.Fatalf("updater client bytes=%d, want 0", p.writes[4350])
		}
		for _, port := range []uint16{4554, 4555} {
			c := connect(port)
			got, err := io.ReadAll(c)
			if len(got) != 0 || err != nil {
				t.Fatalf("refused port %d leaked bytes/error: %q, %v; want empty EOF", port, got, err)
			}
			exchange()
			checkLease()
			inspectStack(t, x)
		}
		// New channels still open on the existing session after both refusals.
		fresh := connect(4350)
		if _, err := io.ReadFull(fresh, greeting); err != nil || string(greeting) != observedUpdaterGreeting {
			t.Fatalf("new sibling after refusals=%q, %v", greeting, err)
		}
		checkLease()
		for port, want := range map[uint16]int{80: 1, 4350: 2, 4554: 1, 4555: 1, 4700: 1} {
			if p.opens[port] != want {
				t.Errorf("port %d connects=%d, want %d", port, p.opens[port], want)
			}
		}
		// Independent literal framing expectations: DATA bytes, clean END,
		// and connect-failure CLOSE result 1 (no positive SETUP ACK).
		wantData := append([]byte{2, 0, byte(len(observedHTTPHeader)), 0}, []byte(observedHTTPHeader)...)
		if frames := p.frames[80]; len(frames) != 2 || !bytes.Equal(frames[0], wantData) || !bytes.Equal(frames[1], []byte{3, 0, 0, 0}) {
			t.Errorf("HTTP peer frames=%x, want DATA %x then 03000000", frames, wantData)
		}
		for _, port := range []uint16{4554, 4555} {
			if frames := p.frames[port]; len(frames) != 1 || !bytes.Equal(frames[0], []byte{4, 0, 1, 0, 1}) {
				t.Errorf("refused port %d frames=%x, want 0400010001", port, frames)
			}
		}
		// Advance fake time past ordinary retry intervals with no client write.
		<-time.After(10 * time.Second)
		exchange()
		checkLease()
		cancel()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if cloud.stops.Load() != 1 || cloud.allocs.Load() != 1 || cloud.logins.Load() != 1 {
			t.Fatalf("final lease counts allocation/login/stop=%d/%d/%d, want 1/1/1", cloud.allocs.Load(), cloud.logins.Load(), cloud.stops.Load())
		}
	})
}
