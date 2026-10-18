package proxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestPendingPatternsAndSiteHandoff(t *testing.T) {
	smb := append([]byte{0, 0, 0, 64, 0xfe, 'S', 'M', 'B', 64, 0, 0, 0}, bytes.Repeat([]byte{0, 255, 37, 19}, 7000)...)
	rpc := []byte("{\"id\":1,\"method\":\"get_device_state\",\"params\":{\"keys\":[\"client\",\"camera\",\"second_camera\"]}}\r\n")
	setter := []byte("{\"id\":2,\"method\":\"set_setting\",\"params\":{\"lat\":1,\"lon\":2,\"force\":true}}\r\n")
	// A line boundary plus setter spans the 8 KiB staging boundary. CRLF remains intact.
	prefix := append(bytes.Repeat([]byte{' '}, 8179), '\r', '\n')
	cases := []struct {
		name     string
		port     uint16
		data     []byte
		stripped bool
	}{
		{"smb-client-first", 445, smb, false},
		{"app-client-first", 4700, rpc, false},
		{"app-site", 4700, append(append(bytes.Clone(prefix), setter...), rpc...), true},
		{"smb-byte-transparent", 445, setter, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a, client := net.Pipe()
				defer client.Close()
				local := &admissionReadConn{Conn: a, data: bytes.Clone(tc.data), terminal: io.EOF}
				l := &e2eSingleListener{conn: local, done: make(chan struct{})}
				d := admissionGateDial{make(chan admissionDialRequest)}
				finished := make(chan error, 1)
				go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: tc.port}}, 1, nil) }()
				defer func() {
					cancel()
					if err := <-finished; err != nil {
						t.Error(err)
					}
				}()
				req := <-d.requests
				synctest.Wait()
				if req.ctx.Err() != nil {
					t.Fatal("FIN canceled pending demand")
				}
				remote, device := net.Pipe()
				defer device.Close()
				end := make(chan struct{})
				req.result <- &admissionHalfConn{remote, end}
				gotDone := make(chan []byte, 1)
				go func() {
					var got []byte
					buf := make([]byte, 3)
					for {
						n, err := device.Read(buf)
						got = append(got, buf[:n]...)
						if err != nil {
							break
						}
					}
					gotDone <- got
				}()
				<-end
				device.SetReadDeadline(time.Now())
				got := <-gotDone
				if tc.stripped && tc.port == 4700 {
					if !bytes.HasPrefix(got, prefix) || !bytes.HasSuffix(got, rpc) {
						t.Fatalf("boundary or following RPC changed: %d bytes", len(got))
					}
					line := got[len(prefix) : len(got)-len(rpc)]
					if string(line) != `{"id":2,"method":"set_setting","params":{"force":true}}`+"\r\n" {
						t.Fatalf("stripped settings=%s", line)
					}

				} else if !bytes.Equal(got, tc.data) {
					t.Fatalf("got %d bytes want %d, stream changed", len(got), len(tc.data))
				}
				response := []byte("device-reply-after-client-FIN")
				go func() { device.Write(response); device.Close() }()
				reply, err := io.ReadAll(client)
				if err != nil || !bytes.Equal(reply, response) {
					t.Fatalf("reverse after FIN=%q %v", reply, err)
				}
			})
		})
	}
}

func TestSilentClientServerFirstAndJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		a, client := net.Pipe()
		defer client.Close()
		l := &e2eSingleListener{conn: a, done: make(chan struct{})}
		d := admissionGateDial{make(chan admissionDialRequest)}
		finished := make(chan error, 1)
		go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: 4700}}, 1, nil) }()
		req := <-d.requests
		synctest.Wait()
		if req.ctx.Err() != nil {
			t.Fatal("silent pending client canceled")
		}
		remote, device := net.Pipe()
		defer device.Close()
		req.result <- remote
		sent := make(chan error, 1)
		go func() { _, err := device.Write([]byte("server-first")); sent <- err }()
		buf := make([]byte, 12)
		if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "server-first" {
			t.Fatalf("server first=%q %v", buf, err)
		}
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	})
}

func TestReaderTinyReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := net.Pipe()
		defer b.Close()
		want := bytes.Repeat([]byte{0, 1, 2, 3, 254, 255}, 6000)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := preread(&admissionReadConn{Conn: a, data: bytes.Clone(want), terminal: io.EOF}, cancel)
		defer r.close()
		synctest.Wait()
		var got []byte
		var buf [1]byte
		for {
			n, err := r.Read(buf[:])
			got = append(got, buf[:n]...)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("tiny reads changed bytes: got %d want %d", len(got), len(want))
		}
		if ctx.Err() != nil {
			t.Fatal("FIN canceled context")
		}
	})
}

func TestResetRemovesLastLazyClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, b := net.Pipe()
		defer b.Close()
		reset := make(chan struct{})
		l := &e2eSingleListener{conn: &e2eResetConn{a, reset}, done: make(chan struct{})}
		s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 257), done: make(chan struct{})}
		finished := make(chan error, 1)
		go func() { finished <- Serve(ctx, s, []Forward{{Listener: l, Port: 4700}}, 1, nil) }()
		defer func() {
			cancel()
			if err := <-finished; err != nil {
				t.Error(err)
			}
		}()
		cfg := session.DefaultConfig()
		cfg.Idle = time.Second
		m, err := session.New(cfg, admissionRNG{})
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Step(session.Event{Kind: session.Start}, 0)) != 0 {
			t.Fatal("lazy startup dialed")
		}
		first := <-s.in
		m.Step(first, 1)
		if m.Snapshot().Clients != 1 {
			t.Fatal("no initial demand")
		}
		close(reset)
		synctest.Wait()
		select {
		case gone := <-s.in:
			before := m.Snapshot()
			m.Step(gone, 2)
			s.Observe(gone, before, m.Snapshot(), 2)
		default:
			t.Fatal("reset left last lazy client pending")
		}
		if m.Snapshot().Clients != 0 || m.Snapshot().Queued != 0 || len(s.clients) != 0 {
			t.Fatalf("stale lazy snapshot=%+v registrations=%d", m.Snapshot(), len(s.clients))
		}
	})
}
