package capturetrace

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"github.com/go-json-experiment/json"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/tracediff"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

// Independent literals model the modern envelope and inner header.
// No production vendor encoder creates this oracle. Everything is synthetic.
func plainPacket(extra bool) []byte {
	h := "04021d003c0000000c10180000000000"
	body := "01000000" + hex.EncodeToString([]byte("SYNTHETIC-UID-000000")) + "00000000000000000000000000000000" + "02002800c00002090000000000000000" + "00000000"
	// UID20 above is exactly 20 bytes.
	if extra {
		h = "04021d00400000000c10180000000000"
		body += "deadbeef"
	}
	b, err := hex.DecodeString(h + body)
	if err != nil {
		panic(err)
	}
	return b
}

func syntheticKeylog() string {
	return "# IOTC_KEY 1 53594e5448455449 3 " + strings.Repeat("11", 16) + " " + strings.Repeat("33", 12) + "\n"
}

func encrypted(t testing.TB, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(bytes.Repeat([]byte{0x11}, 16))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	// Both literal test frames fit in one byte of the envelope's LE16 length.
	header := []byte{0x51, 0xcc, 0, 0, 2, 1, byte(len(plain) + 16), 0, 7, 0, 0, 0}
	return aead.Seal(header, bytes.Repeat([]byte{0x33}, 12), plain, header)
}

func capture(t testing.TB, link layers.LinkType, bodies ...[]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := pcapgo.NewWriter(&b)
	if err := w.WriteFileHeader(65535, link); err != nil {
		t.Fatal(err)
	}
	for i, body := range bodies {
		eth := &layers.Ethernet{SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{2, 0, 0, 0, 0, 2}, EthernetType: layers.EthernetTypeIPv4}
		ip := &layers.IPv4{Version: 4, TTL: 64, Protocol: layers.IPProtocolUDP, SrcIP: net.IPv4(192, 0, 2, 9), DstIP: net.IPv4(192, 0, 2, 10)}
		udp := &layers.UDP{SrcPort: 54321, DstPort: 54322}
		if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
			t.Fatal(err)
		}
		buf := gopacket.NewSerializeBuffer()
		if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, eth, ip, udp, gopacket.Payload(body)); err != nil {
			t.Fatal(err)
		}
		data := buf.Bytes()
		if err := w.WritePacket(gopacket.CaptureInfo{Timestamp: time.Unix(100, int64(i)*1000000), CaptureLength: len(data), Length: len(data)}, data); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}

type traceSink struct{ bytes.Buffer }

func (*traceSink) Close() error { return nil }

func recording(t *testing.T) []byte {
	t.Helper()
	var s traceSink
	w := tracerecord.New(&s, tracerecord.SizeCap, func() time.Time { return time.Unix(0, 0) })
	for i, extra := range []bool{false, true} {
		w.Record("iotc", 1, core.Instant(i*1000000), iotc.Event{Kind: iotc.Wire, Data: plainPacket(extra), Link: iotc.Link{Transport: iotc.UDP, Address: netip.MustParseAddrPort("192.0.2.9:54321")}}, []iotc.Action{})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Dropped() != 0 {
		t.Fatal("synthetic recording dropped")
	}
	return append([]byte(nil), s.Bytes()...)
}

func status(t *testing.T, left, right []byte) string {
	t.Helper()
	b, err := tracediff.Compare(bytes.NewReader(left), bytes.NewReader(right), tracediff.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ Status string }
	if err := json.Unmarshal(b, &r, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	return r.Status
}

func TestSyntheticEqual(t *testing.T) {
	p0, p1 := plainPacket(false), plainPacket(true)
	if len(p0) != 76 || len(p1) != 80 {
		t.Fatalf("literal extents %d, %d", len(p0), len(p1))
	}
	c := capture(t, layers.LinkTypeEthernet, encrypted(t, p0), encrypted(t, p1))
	got, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1})
	if err != nil || got.Incomplete {
		t.Fatalf("decode: incomplete=%v err=%v", got.Incomplete, err)
	}
	if s := status(t, got.JSONL, recording(t)); s != "equal" {
		t.Fatalf("status %s want equal", s)
	}
	rs, err := tracerecord.Read(bytes.NewReader(got.JSONL))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 3 || rs[0].Input.Length != 76 || rs[0].Input.Type != 0x100c || rs[1].Input.Length != 80 {
		t.Fatalf("literal wire metadata: %+v", rs)
	}
	for _, needle := range []string{"SYNTHETIC", "192.0.2", "54321", "54322", strings.Repeat("11", 16), strings.Repeat("33", 12), "53594e5448455449", "deadbeef"} {
		if bytes.Contains(got.JSONL, []byte(needle)) {
			t.Fatal("sensitive sentinel escaped")
		}
	}
}

func TestReorderedPacket(t *testing.T) {
	c := capture(t, layers.LinkTypeEthernet, encrypted(t, plainPacket(true)), encrypted(t, plainPacket(false)))
	got, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Incomplete || status(t, got.JSONL, recording(t)) != "different" {
		t.Fatal("packet order was erased")
	}
}

func TestUnavailableKeys(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		kind      Failure
	}{
		{"missing", "", MissingKey}, {"wrong", strings.Replace(syntheticKeylog(), strings.Repeat("11", 16), strings.Repeat("22", 16), 1), Authentication},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := capture(t, layers.LinkTypeEthernet, encrypted(t, plainPacket(false)))
			got, err := Decode(bytes.NewReader(c), strings.NewReader(tc.key), Options{Attempt: 1})
			var bad *DecodeError
			if !errors.As(err, &bad) || bad.Kind != tc.kind || !got.Incomplete {
				t.Fatalf("got %v incomplete=%v", err, got.Incomplete)
			}
			if status(t, got.JSONL, recording(t)) != "inconclusive" {
				t.Fatal("missing authenticated evidence claimed")
			}
			rs, e := tracerecord.Read(bytes.NewReader(got.JSONL))
			if e != nil || len(rs) != 1 || rs[0].Record != "end" {
				t.Fatal("fabricated an event")
			}
		})
	}
}

func TestCaptureFailures(t *testing.T) {
	good := capture(t, layers.LinkTypeEthernet, encrypted(t, plainPacket(false)), encrypted(t, plainPacket(true)))
	for _, tc := range []struct {
		name  string
		input []byte
		kind  Failure
	}{
		{"truncated", good[:len(good)-1], MalformedCapture},
		{"unknown_link", capture(t, layers.LinkType(147)), UnsupportedLink},
		{"truncated_header", good[:12], MalformedCapture},
		{"malformed_vendor", capture(t, layers.LinkTypeEthernet, []byte{1, 2, 3}), MalformedVendor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decode(bytes.NewReader(tc.input), strings.NewReader(syntheticKeylog()), Options{Attempt: 1})
			var bad *DecodeError
			if !errors.As(err, &bad) || bad.Kind != tc.kind || !got.Incomplete {
				t.Fatalf("got %v incomplete=%v", err, got.Incomplete)
			}
			if status(t, got.JSONL, recording(t)) != "inconclusive" {
				t.Fatal("partial decode claimed complete")
			}
		})
	}
}

func TestEachCap(t *testing.T) {
	// A distinct destination port creates a second directional flow.
	one := capture(t, layers.LinkTypeEthernet, encrypted(t, plainPacket(false)), encrypted(t, plainPacket(true)))
	flow := rewriteCapture(t, one, func(i int, _ *layers.IPv4, udp *layers.UDP) {
		if i == 1 {
			udp.DstPort++
		}
	})
	for _, tc := range []struct {
		name, resource string
		limits         Limits
		input          []byte
	}{
		{"bytes", "capture bytes", Limits{CaptureBytes: len(one) - 1}, one},
		{"packets", "packets", Limits{Packets: 1}, one},
		{"flows", "flows", Limits{Flows: 1}, flow},
		{"records", "decrypted records", Limits{DecryptedRecords: 1}, one},
		{"output", "output bytes", Limits{OutputBytes: 200}, one},
		{"keylog", "keylog bytes", Limits{KeylogBytes: 1}, one},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decode(bytes.NewReader(tc.input), strings.NewReader(syntheticKeylog()), Options{Attempt: 1, Limits: tc.limits})
			var cap *LimitError
			if !errors.As(err, &cap) || cap.Resource != tc.resource || !got.Incomplete {
				t.Fatalf("got %v incomplete=%v", err, got.Incomplete)
			}
			if len(got.JSONL) > 0 && status(t, got.JSONL, recording(t)) != "inconclusive" {
				t.Fatal("capped evidence claimed complete")
			}
		})
	}
}

func TestBoundaryCaps(t *testing.T) {
	c := capture(t, layers.LinkTypeEthernet, encrypted(t, plainPacket(false)))
	base, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1, Limits: Limits{CaptureBytes: len(c), Packets: 1, Flows: 1, DecryptedRecords: 1, OutputBytes: len(base.JSONL), KeylogBytes: len(syntheticKeylog())}})
	if err != nil || got.Incomplete || !bytes.Equal(base.JSONL, got.JSONL) {
		t.Fatalf("inclusive caps: %v", err)
	}
}
