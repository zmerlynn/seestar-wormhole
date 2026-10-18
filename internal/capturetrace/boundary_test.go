package capturetrace

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

func TestPacketBodyAbsent(t *testing.T) {
	c := capture(t, layers.LinkTypeEthernet, encrypted(t, plainPacket(false)))
	// Retain the library-written global and packet headers, with zero body bytes.
	c = c[:40]
	got, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1})
	var bad *DecodeError
	if !errors.As(err, &bad) || bad.Kind != MalformedCapture || bad.Packet != 1 || !got.Incomplete {
		t.Fatalf("missing body: err=%v incomplete=%v", err, got.Incomplete)
	}
	if status(t, got.JSONL, recording(t)) != "inconclusive" {
		t.Fatal("absent packet body claimed complete")
	}
}

func TestPacketBudgetBeforeBody(t *testing.T) {
	// A complete library-written header declares a body larger than the whole
	// budget. The absent body must never be allocated or read first.
	c := capture(t, layers.LinkTypeEthernet, bytes.Repeat([]byte{0}, 256))[:40]
	got, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1, Limits: Limits{CaptureBytes: 64}})
	var cap *LimitError
	if !errors.As(err, &cap) || cap.Resource != "capture bytes" || cap.Limit != 64 || !got.Incomplete {
		t.Fatalf("packet budget before body: err=%v incomplete=%v", err, got.Incomplete)
	}
	if status(t, got.JSONL, recording(t)) != "inconclusive" {
		t.Fatal("oversized declaration claimed complete")
	}
}

func TestPacketAllocationBound(t *testing.T) {
	c := capture(t, layers.LinkTypeEthernet, bytes.Repeat([]byte{0}, 60000))[:40]
	decode := func() {
		_, _ = Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1, Limits: Limits{CaptureBytes: 64}})
	}
	decode() // Warm JSON/error metadata before measuring fixed decoding overhead.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 32; i++ {
		decode()
	}
	runtime.ReadMemStats(&after)
	// Allow ample fixed library overhead, but never a declared 60 KB body per
	// call when the entire input/expanded-packet budget is only 64 bytes.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32*(32<<10) {
		t.Fatalf("allocated %d bytes for 32 tiny-budget decodes, want <= %d", allocated, 32*(32<<10))
	}
}

// rewriteCapture uses only the capture/layer library to vary synthetic headers.
func rewriteCapture(t *testing.T, c []byte, change func(int, *layers.IPv4, *layers.UDP)) []byte {
	t.Helper()
	r, err := pcapgo.NewReader(bytes.NewReader(c))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w := pcapgo.NewWriter(&out)
	if err := w.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		data, ci, err := r.ReadPacketData()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var eth layers.Ethernet
		var ip layers.IPv4
		var udp layers.UDP
		p := gopacket.NewDecodingLayerParser(layers.LayerTypeEthernet, &eth, &ip, &udp)
		p.IgnoreUnsupported = true
		var decoded []gopacket.LayerType
		if err := p.DecodeLayers(data, &decoded); err != nil {
			t.Fatal(err)
		}
		change(i, &ip, &udp)
		if err := udp.SetNetworkLayerForChecksum(&ip); err != nil {
			t.Fatal(err)
		}
		buf := gopacket.NewSerializeBuffer()
		if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, &eth, &ip, &udp, gopacket.Payload(udp.Payload)); err != nil {
			t.Fatal(err)
		}
		ci.CaptureLength, ci.Length = len(buf.Bytes()), len(buf.Bytes())
		if err := w.WritePacket(ci, buf.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func TestLayerScope(t *testing.T) {
	base := capture(t, layers.LinkTypeEthernet, encrypted(t, plainPacket(false)))
	for _, tc := range []struct {
		name   string
		change func(int, *layers.IPv4, *layers.UDP)
		kind   Failure
	}{
		{"fragment", func(_ int, ip *layers.IPv4, _ *layers.UDP) { ip.Flags |= layers.IPv4MoreFragments }, UnsupportedLayer},
		{"wrong_ip_version", func(_ int, ip *layers.IPv4, _ *layers.UDP) { ip.Version = 6 }, MalformedCapture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := rewriteCapture(t, base, tc.change)
			got, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1})
			var bad *DecodeError
			if !errors.As(err, &bad) || bad.Kind != tc.kind || !got.Incomplete || status(t, got.JSONL, recording(t)) != "inconclusive" {
				t.Fatalf("err=%v incomplete=%v", err, got.Incomplete)
			}
		})
	}
}

func TestCompressedCaptureBound(t *testing.T) {
	var bodies [][]byte
	for i := 0; i < 32; i++ {
		bodies = append(bodies, encrypted(t, plainPacket(false)))
	}
	c := capture(t, layers.LinkTypeEthernet, bodies...)
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(c); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() >= 1024 {
		t.Fatal("fixture does not exercise expansion")
	}
	got, err := Decode(&compressed, strings.NewReader(syntheticKeylog()), Options{Attempt: 1, Limits: Limits{CaptureBytes: 1024}})
	var cap *LimitError
	if !errors.As(err, &cap) || cap.Resource != "capture bytes" || !got.Incomplete || status(t, got.JSONL, recording(t)) != "inconclusive" {
		t.Fatalf("expanded capture: %v incomplete=%v", err, got.Incomplete)
	}
}

func TestTooSmallOutputAndInvalidLimits(t *testing.T) {
	c := capture(t, layers.LinkTypeEthernet, encrypted(t, plainPacket(false)))
	got, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1, Limits: Limits{OutputBytes: 1}})
	var cap *LimitError
	if !errors.As(err, &cap) || cap.Resource != "output bytes" || len(got.JSONL) != 0 || !got.Incomplete {
		t.Fatalf("tiny output cap: %v incomplete=%v bytes=%d", err, got.Incomplete, len(got.JSONL))
	}
	got, err = Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Limits: Limits{Packets: -1}})
	if err == nil || !got.Incomplete || len(got.JSONL) != 0 {
		t.Fatalf("invalid limits: %v incomplete=%v bytes=%d", err, got.Incomplete, len(got.JSONL))
	}
}

func TestAliasCaps(t *testing.T) {
	var bodies [][]byte
	for i := 0; i < 129; i++ {
		bodies = append(bodies, encrypted(t, plainPacket(false)))
	}
	base := capture(t, layers.LinkTypeEthernet, bodies...)
	for _, alias := range []string{"peer", "port"} {
		t.Run(alias, func(t *testing.T) {
			c := rewriteCapture(t, base, func(i int, ip *layers.IPv4, udp *layers.UDP) {
				if alias == "peer" {
					ip.SrcIP = []byte{192, 0, 2, byte(i + 1)}
				} else {
					udp.SrcPort = layers.UDPPort(54000 + i)
				}
			})
			got, err := Decode(bytes.NewReader(c), strings.NewReader(syntheticKeylog()), Options{Attempt: 1, Limits: Limits{Flows: 256}})
			var cap *LimitError
			if !errors.As(err, &cap) || cap.Resource != "aliases" || cap.Limit != 128 || !got.Incomplete {
				t.Fatalf("alias overflow: %v incomplete=%v", err, got.Incomplete)
			}
			rs, err := tracerecord.Read(bytes.NewReader(got.JSONL))
			if err != nil || len(rs) != 129 || rs[128].Dropped != 1 || status(t, got.JSONL, recording(t)) != "inconclusive" {
				t.Fatal("alias cap lost the bounded authenticated prefix or loss marker")
			}
		})
	}
}
