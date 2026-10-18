package landiscovery

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"net/netip"
	"testing"
	"time"
)

type traceSink struct{ bytes.Buffer }

func (*traceSink) Close() error { return nil }
func TestLANTraceRedactsIdentityAndScanBytes(t *testing.T) {
	m, _, err := New("secret-serial", "secret-name", "secret-model")
	if err != nil {
		t.Fatal(err)
	}
	sink := new(traceSink)
	w := tracerecord.New(sink, 4096, time.Now)
	a := netip.MustParseAddr("192.0.2.7")
	s := &Socket{Trace: w, interfaces: map[int][]netip.Prefix{1: {netip.MustParsePrefix("192.0.2.1/24")}}, addresses: func(int) []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("192.0.2.1/24")} }}
	reply, _ := s.response(m, []byte(`{"id":7654321,"method":"scan_iscope","params":{"password":"secret-params"}}`), 0, 1, a, netip.MustParseAddr("192.0.2.1"))
	if reply == nil {
		t.Fatal("missing reply")
	}
	w.Close()
	for _, secret := range []string{"secret-", "192.0.2.", "7654321", "Timestamp"} {
		if bytes.Contains(sink.Bytes(), []byte(secret)) {
			t.Fatalf("leaked %s", secret)
		}
	}
	rr, err := tracerecord.Read(bytes.NewReader(sink.Bytes()))
	if err != nil || len(rr) != 2 || rr[0].Outputs[0].Length != len(reply) {
		t.Fatalf("trace %s %v", sink.Bytes(), err)
	}
}
