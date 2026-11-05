package ui

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"testing/fstest"
	"time"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// A Browse download outlasts the server's write timeout, which would
// otherwise cut it off partway: the reader here waits past it before
// reading a file larger than the socket's buffers.
func TestBrowseDownloadOutlastsWriteTimeout(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(t)
	s.host = listener.Addr().String()
	big := make([]byte, 64<<20)
	s.copy = copyapp.NewMount(context.Background(), copyapp.MountOptions{
		Addr: netip.MustParseAddrPort(s.host), StateDir: t.TempDir(), Version: "synthetic",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: s.copyTarget,
		Dial: func(context.Context, string) (remote.Session, error) {
			return remote.NewFake(fstest.MapFS{"MyWorks/big.fit": {Data: big}}), nil
		},
		Test: func(context.Context, string) ([]remote.Entry, error) {
			return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
		},
	})
	t.Cleanup(s.copy.Quit)
	copySettings(t, s)
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(s)
	server.Listener.Close()
	server.Listener = listener
	server.Config = newHTTPServer(s)
	server.Config.WriteTimeout = 200 * time.Millisecond
	server.Start()
	defer server.Close()
	response, err := http.Get(server.URL + "/browse/file?path=MyWorks/big.fit&download=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	time.Sleep(time.Second)
	n, err := io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 || err != nil || n != int64(len(big)) {
		t.Fatalf("download = %d, %d of %d bytes, %v", response.StatusCode, n, len(big), err)
	}
}
