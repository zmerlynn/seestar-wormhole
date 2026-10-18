package proxy

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Events can precede every reply or interleave in a pipelined batch. Identical
// ids on different connections cannot cross-route; reply ids need not be FIFO.
func TestControlPerClientInterleaving(t *testing.T) {
	const event = "{\"Event\":\"PiStatus\",\"Timestamp\":\"1.000000001\",\"temp\":20}\r\n"
	const repliesA = "{\"id\":9,\"code\":0,\"result\":\"A-third\"}\r\n" + event +
		"{\"id\":7,\"code\":0,\"result\":\"A-first\"}\r\n{\"id\":8,\"code\":0,\"result\":\"A-second\"}\r\n"
	const repliesB = "{\"id\":8,\"code\":0,\"result\":\"B-second\"}\r\n" + event +
		"{\"id\":9,\"code\":0,\"result\":\"B-third\"}\r\n{\"id\":7,\"code\":0,\"result\":\"B-first\"}\r\n"
	const batch = "{\"id\":7,\"method\":\"get_device_state\",\"client\":\"CLIENT\"}\r\n" +
		"{\"id\":8,\"method\":\"get_device_state\",\"client\":\"CLIENT\"}\r\n" +
		"{\"id\":9,\"method\":\"get_device_state\",\"client\":\"CLIENT\"}\r\n"
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		configure := func(p *simulatedWire) {
			requests := make(map[byte]int)
			clients := make(map[byte]string)
			p.controlReply = func(ch byte, line []byte) []byte {
				var request struct {
					ID     int
					Client string
				}
				if err := json.Unmarshal(line, &request, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Error(err)
					return nil
				}
				expectedID := 7 + requests[ch]
				if request.ID != expectedID || requests[ch] > 0 && clients[ch] != request.Client {
					t.Errorf("channel %d request id/client=%d/%s, want %d/%s", ch, request.ID, request.Client, expectedID, clients[ch])
				}
				clients[ch] = request.Client
				requests[ch]++
				if requests[ch] != 3 {
					return nil
				}
				switch request.Client {
				case "A":
					return []byte(event + repliesA)
				case "B":
					return []byte(event + repliesB)
				default:
					t.Errorf("unexpected client %q", request.Client)
					return nil
				}
			}
		}
		s, _, _ := workloadSession(t, ctx, configure)
		defer s.Close()
		var connections []net.Conn
		for _, client := range []string{"A", "B"} {
			c, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second))
			connections = append(connections, c)
			if _, err := c.Write([]byte(strings.ReplaceAll(batch, "CLIENT", client))); err != nil {
				t.Fatal(err)
			}
			if err := c.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
				t.Fatal(err)
			}
		}
		for i, expected := range []string{event + repliesA, event + repliesB} {
			var out bytes.Buffer
			writer := &siteClientWriter{writer: &out}
			if err := writer.copyDevice(connections[i]); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != expected {
				t.Fatalf("client %d stream attributed, reordered, filtered or crossed: got %q want %q", i, got, expected)
			}
		}
	})
}
