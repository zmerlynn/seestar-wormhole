package tracerecord

import (
	"fmt"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
)

type discardSink struct{}

func (discardSink) Write(b []byte) (int, error) { return io.Discard.Write(b) }
func (discardSink) Close() error                { return nil }

// Fixed goroutine counts exercise the inline hook, including lock competition.
// The sink discards bytes; producers never acknowledge or await storage. Loss
// metrics distinguish cheap rejections from completed projection/encoding work.
func BenchmarkProducer(b *testing.B) {
	for _, actions := range []int{1, 11, 2048} {
		for _, producers := range []int{1, 2, 4} {
			b.Run(fmt.Sprintf("actions=%d/producers=%d", actions, producers), func(b *testing.B) {
				w := New(discardSink{}, math.MaxInt, time.Now)
				input := rdt.Event{Kind: rdt.Receive, Packet: rdt.Packet{Type: rdt.Data, Payload: make([]byte, 1024)}}
				output := make([]rdt.Action, actions)
				for i := range output {
					output[i] = rdt.Action{Kind: rdt.ArmTimer, At: 123456}
				}
				var wg sync.WaitGroup
				b.ReportAllocs()
				b.ResetTimer()
				for p := range producers {
					wg.Go(func() {
						for i := p; i < b.N; i += producers {
							w.Record("rdt", 1, core.Instant(i), input, output)
						}
					})
				}
				wg.Wait()
				b.StopTimer()
				w.Close()
				b.ReportMetric(float64(w.causes.contention.Load())/float64(b.N), "contention/op")
				b.ReportMetric(float64(w.causes.queueFull.Load())/float64(b.N), "queue_full/op")
			})
		}
	}
}

// Mirrors the largest-field transaction-limit test, rather than just a large
// array of mostly empty summaries. This bounds the expensive rejection path.
func BenchmarkProducerEncodedLimit(b *testing.B) {
	w := New(discardSink{}, math.MaxInt, time.Now)
	actions := make([]p2ptunnel.Action, 2048)
	for i := range actions {
		actions[i] = p2ptunnel.Action{Kind: p2ptunnel.WriteLocal, Channel: 255, Generation: math.MaxUint64, Token: math.MaxUint64, Request: math.MaxUint64, At: math.MaxInt64}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		w.Record("tunnel", 1, 0, p2ptunnel.Event{}, actions)
	}
	b.StopTimer()
	w.Close()
}
