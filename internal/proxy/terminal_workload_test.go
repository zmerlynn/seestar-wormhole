package proxy

import (
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"testing"
	"time"
)

func canceledBurst(s *Session, ctx context.Context, count int) error {
	dead, stop := context.WithCancel(ctx)
	stop()
	for i := 0; i < count; i++ {
		dial := context.Context(dead)
		racing, cancel := context.WithCancel(ctx)
		if i%2 == 0 {
			dial = cancelAtSelect{racing, cancel}
		}
		c, err := s.DialPort(dial, 4700)
		cancel()
		if c != nil {
			c.Close()
			return fmt.Errorf("canceled admission returned stream")
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, session.ErrBudget) {
			return fmt.Errorf("canceled admission: %w", err)
		}
	}
	return nil
}

func rejectedSetup(s *Session, ctx context.Context, verified bool) error {
	c, err := s.DialPort(ctx, 12345)
	if err == nil {
		defer c.Close()
		_, err = io.ReadAll(c)
	}
	if verified {
		if !errors.Is(err, session.ErrTransport) {
			return fmt.Errorf("preflight rejection: %v", err)
		}
	} else {
		var peer *PeerCloseError
		if !errors.Is(err, ErrSetupRejected) || !errors.As(err, &peer) || peer.Result != 2 {
			return fmt.Errorf("SETUP rejection lost CLOSE result 2: %v", err)
		}
	}
	return nil
}

func endingExchange(c *stream, id int, finish string, chunk int, delay time.Duration) error {
	c.SetDeadline(time.Now().Add(30 * time.Second))
	request := fmt.Sprintf("{\"id\":%d,\"method\":\"get_device_state\",\"finish\":%q}\n", id, finish)
	if _, err := c.Write([]byte(request)); err != nil {
		return err
	}
	if delay > 0 {
		time.Sleep(delay)
	} // virtual time in composed synctest workload
	var got []byte
	b := make([]byte, chunk)
	for {
		n, err := c.Read(b)
		got = append(got, b[:n]...)
		if len(got) > 4096 {
			return fmt.Errorf("unbounded terminal response")
		}
		if err == nil {
			continue
		}
		if finish == "fail" {
			var peer *PeerCloseError
			if !errors.Is(err, session.ErrTransport) || !errors.As(err, &peer) || peer.Result != 1 {
				return fmt.Errorf("peer failure lost cause: %v", err)
			}
		} else if err != io.EOF {
			return fmt.Errorf("clean peer ending: %v", err)
		}
		break
	}
	want := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"state\":\"idle\"}}\n", id)
	if string(got) != want {
		return fmt.Errorf("terminal %s delivered %q want %q", finish, got, want)
	}
	return checkResponse(string(got), id)
}

func TestStreamRacingEndAndFailureDrainsPartialReads(t *testing.T) {
	for _, terminal := range []error{io.EOF, session.ErrTransport} {
		for range 10000 {
			s := &Session{done: make(chan struct{}), work: make(chan func(core.Instant), 1)}
			c := newStream(s, 1, 4700)
			read := make(chan error, 1)
			go func() {
				var got []byte
				b := make([]byte, 1)
				for {
					n, err := c.Read(b)
					got = append(got, b[:n]...)
					if err != nil {
						if string(got) != "abc" || !errors.Is(err, terminal) {
							read <- fmt.Errorf("read %q %v, want abc %v", got, err, terminal)
						} else {
							read <- nil
						}
						return
					}
				}
			}()
			c.rx <- delivery{action: p2ptunnel.Action{Data: []byte("abc")}}
			if terminal == io.EOF {
				close(c.readEnd)
			} else {
				c.finish(terminal)
			}
			if err := <-read; err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPublishedFailureOverridesEnd(t *testing.T) {
	for range 100 {
		c := newStream(&Session{done: make(chan struct{})}, 1, 4700)
		c.rx <- delivery{action: p2ptunnel.Action{Data: []byte("abc")}}
		close(c.readEnd)
		c.finish(session.ErrTransport)
		b, err := io.ReadAll(c)
		if string(b) != "abc" || !errors.Is(err, session.ErrTransport) {
			t.Fatalf("published failure with END: %q %v", b, err)
		}
	}
}
