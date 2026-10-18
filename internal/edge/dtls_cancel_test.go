package edge

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

// Delay propagation to child contexts while letting the lifetime watcher see
// parent cancellation. A context's Done closes before child cancellation has
// necessarily completed; AfterFunc callbacks can likewise be scheduled later.
type delayedDTLSContext struct {
	context.Context
	callback chan func()
}

// Hide the embedded cancel context so WithTimeout uses the AfterFunc contract.
func (c delayedDTLSContext) Value(any) any { return nil }
func (c delayedDTLSContext) AfterFunc(f func()) func() bool {
	c.callback <- f
	return func() bool { return true }
}

func TestDTLSParentCancellationBeforeChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx := delayedDTLSContext{parent, make(chan func(), 1)}
		p := packetPath(t, 1388)
		done := make(chan error, 1)
		go func() {
			c, err := DialDTLS(ctx, p, []byte("test"), []byte("password"))
			if c != nil {
				c.Close()
			}
			done <- err
		}()
		<-p.Outgoing()
		propagate := <-ctx.callback
		cancel()
		err := <-done
		propagate()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("parent canceled before handshake child: got %v, want context.Canceled", err)
		}
	})
}
