package edge

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	pion "github.com/pion/dtls/v3"
	"github.com/pion/logging"
	profile "github.com/zmerlynn/seestar-wormhole/internal/dtls"
)

// Hold the real library reader after packet I/O has returned, inside its
// existing logger hook. PacketConn.Close can complete but the reader cannot.
// This makes a missing library join fail independently of scheduling.
type exitGateLogger struct {
	quietDTLSLogger
	enabled          atomic.Bool
	reached, release chan struct{}
}

func (l *exitGateLogger) NewLogger(string) logging.LeveledLogger { return l }
func (l *exitGateLogger) Debugf(format string, _ ...any) {
	if l.enabled.Load() && strings.Contains(format, "decrypt failed") {
		close(l.reached)
		<-l.release
	}
}
func TestDTLSCloseWaitsForReaderExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, b := packetPath(t, 1388), packetPath(t, 1388)
		gate := &exitGateLogger{reached: make(chan struct{}), release: make(chan struct{})}
		var fragmented atomic.Bool
		wg := linkDTLS(ctx, a, b, false, nil, &fragmented, func(err error) {
			t.Errorf("DTLS test link: %v", err)
			cancel()
		})
		id, key, _ := profile.Credentials([]byte("test"), []byte("password"))
		var clientOptions []pion.ClientOption
		var serverOptions []pion.ServerOption
		for _, o := range dtlsOptions(id, key, 1388) {
			clientOptions = append(clientOptions, o)
			serverOptions = append(serverOptions, o)
		}
		clientOptions = append(clientOptions, pion.WithLoggerFactory(gate))
		serverOptions = append(serverOptions, pion.WithPSKIdentityHint([]byte{}))
		client, err := pion.ClientWithOptions(a, dtlsAddr{}, clientOptions...)
		if err != nil {
			t.Fatal(err)
		}
		server, err := pion.ServerWithOptions(b, dtlsAddr{}, serverOptions...)
		if err != nil {
			t.Fatal(err)
		}
		serverReady := make(chan error, 1)
		go func() { serverReady <- server.HandshakeContext(ctx) }()
		if err := client.HandshakeContext(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-serverReady; err != nil {
			t.Fatal(err)
		}
		a.allowApplication()
		gate.enabled.Store(true)
		// Literal DTLS 1.2 application header, epoch 1, sequence 42, invalid AEAD.
		if err := a.Deliver(1, []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 42, 0, 1, 0}); err != nil {
			t.Fatal(err)
		}
		<-gate.reached
		done := make(chan struct{})
		go func() { client.Close(); close(done) }()
		synctest.Wait()
		early := false
		select {
		case <-done:
			early = true
		default:
		}
		close(gate.release)
		<-done
		cancel()
		a.Close()
		b.Close()
		server.Close()
		wg.Wait()
		if early {
			t.Fatal("Close returned while the library reader was still alive")
		}
	})
}
