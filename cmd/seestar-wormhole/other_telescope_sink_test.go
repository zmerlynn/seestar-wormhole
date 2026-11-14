package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// A file command's failure because another telescope answered reaches the
// 16 KiB sink whole, in both formats, whatever text the other telescope's
// reply carried.
func TestOtherTelescopeFailureFitsSink(t *testing.T) {
	del := strings.Repeat("\x7f", 3700)
	for _, other := range []landiscovery.Telescope{
		{Serial: "TEST0002", Name: del, Model: del},
		{Serial: del, Name: del, Model: del},
	} {
		err := fmt.Errorf("find telescope: %w", &landiscovery.OtherTelescopeError{Serial: "SERIAL-A", Address: netip.MustParseAddr("192.0.2.85"), Other: &other})
		if len(err.Error()) > 4096 || !strings.HasSuffix(err.Error(), ", answered at 192.0.2.85 instead of serial SERIAL-A") {
			t.Fatalf("%d bytes: %q", len(err.Error()), err)
		}
		for _, format := range []string{"text", "json"} {
			sink := &serialLogSink{}
			q := newQueuedLog(sink)
			code := mainExitCodeWithRunner(append(fileArgs(t)["sync"], "--log-format", format), io.Discard, q, noSignals,
				func() (io.Writer, func()) { return q, func() {} }, false,
				fakeFileRun(func(io.Writer, *slog.Logger) error { return err }))
			q.close()
			<-q.done
			lines := strings.Split(strings.TrimSpace(sink.String()), "\n")
			record := decodeRecord(t, format, lines[len(lines)-1])
			if code != 1 || q.Dropped() != 0 || record["msg"] != "sync failed" || record["error"] != err.Error() {
				t.Errorf("%s: exit %d, dropped %d, sink %q", format, code, q.Dropped(), sink)
			}
		}
	}
}
