// Package capturetrace decodes a bounded offline wire observation into the
// tracerecord allowlist consumed by tracediff. It never reconstructs Steps or
// internal actions. See README.md for the deliberately narrow supported path.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md
package capturetrace

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"net/netip"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

type flow struct{ src, dst netip.AddrPort }
type decoder struct {
	limits            Limits
	result            Result
	output            []byte
	sequence          uint64
	projector         tracerecord.Projector
	flows             map[flow]uint64
	origin            time.Time
	records, expanded int
}

// Decode reads classic pcap through pcapgo, Ethernet/IPv4/UDP through gopacket,
// and vendor envelopes/inner frames through iotc. Authentication uses Go's
// AES-128-GCM with the key and IV scoped to the selected attempt before inner
// interpretation. Only incoming type 1 master replies are supported.
// No DTLS parser or network I/O is involved.
//
// Capture order and relative timestamps are preserved, never sorted. The
// complete result is scoped to the authenticated wire-only projection, not a
// complete runtime recording or proof of erased payload equality.
func Decode(capture, keylog io.Reader, options Options) (Result, error) {
	l, err := defaults(options.Limits)
	if err != nil {
		return Result{Incomplete: true}, err
	}
	d := decoder{limits: l, flows: make(map[flow]uint64)}
	err = d.decode(capture, keylog, options.Attempt)
	return d.finish(err)
}

func (d *decoder) decode(capture, keylog io.Reader, attempt uint64) error {
	data, err := bounded(capture, d.limits.CaptureBytes, "capture bytes")
	if err != nil {
		return err
	}
	pair, err := readKey(keylog, d.limits.KeylogBytes, attempt)
	if err != nil {
		return err
	}
	r, err := pcapgo.NewReader(bytes.NewReader(data))
	if err != nil {
		return &DecodeError{Kind: MalformedCapture}
	}
	if r.LinkType() != layers.LinkTypeEthernet {
		return &DecodeError{Kind: UnsupportedLink}
	}
	for n := 1; ; n++ {
		remaining := d.limits.CaptureBytes - d.expanded
		// pcapgo checks the decoded capture length against snaplen before
		// allocating or reading the body. Charge its reusable buffer against
		// the remaining budget, never the file's advertised snaplen. Snaplen
		// only decreases, so reading past the packet cap cannot grow storage.
		r.SetSnaplen(min(r.Snaplen(), uint32(min(remaining, 65535))))
		data, ci, err := r.ZeroCopyReadPacketData()
		if ci.CaptureLength > remaining {
			return &LimitError{"capture bytes", d.limits.CaptureBytes}
		}
		// A missing body can also produce plain EOF after pcapgo allocated
		// its declared extent. Only EOF before a packet header is complete.
		if err == io.EOF && data == nil {
			return nil
		}
		if err != nil {
			return &DecodeError{MalformedCapture, n}
		}
		if n > d.limits.Packets {
			return &LimitError{"packets", d.limits.Packets}
		}
		d.expanded += len(data)
		if ci.CaptureLength != ci.Length {
			return &DecodeError{MalformedCapture, n}
		}
		f, wire, err := udpPacket(data)
		if err != nil {
			return &DecodeError{err.(*DecodeError).Kind, n}
		}
		instance, ok := d.flows[f]
		if !ok {
			if len(d.flows) >= d.limits.Flows {
				return &LimitError{"flows", d.limits.Flows}
			}
			instance = uint64(len(d.flows) + 1)
			d.flows[f] = instance
		}
		envelope, err := iotc.ParseRecord(wire)
		if err != nil {
			return &DecodeError{MalformedVendor, n}
		}
		if envelope.Type != 1 {
			return &DecodeError{UnsupportedRecord, n}
		}
		if pair == nil {
			return &DecodeError{MissingKey, n}
		}
		if d.records >= d.limits.DecryptedRecords {
			return &LimitError{"decrypted records", d.limits.DecryptedRecords}
		}
		// AAD is exactly the existing vendor codec's 12-byte envelope. Its parser
		// has already checked the full extent, including the appended tag.
		plain, err := pair.aead.Open(nil, pair.iv[:], envelope.Body, wire[:12])
		if err != nil {
			return &DecodeError{Authentication, n}
		}
		d.records++
		if packet, err := iotc.ParsePacket(plain); err != nil {
			return &DecodeError{MalformedVendor, n}
		} else if packet.Opcode != 0x100c {
			return &DecodeError{UnsupportedRecord, n}
		}
		if d.origin.IsZero() {
			d.origin = ci.Timestamp
		}
		delta := ci.Timestamp.Sub(d.origin)
		// time.Sub saturates; reject unrepresentable relative capture clocks.
		if !d.origin.Add(delta).Equal(ci.Timestamp) {
			return &DecodeError{MalformedCapture, n}
		}
		summary := d.projector.Summarize(iotc.Event{Kind: iotc.Wire, Link: iotc.Link{Transport: iotc.UDP, Address: f.src}, Data: plain})
		if summary.Peer == "peerOverflow" || summary.PortAlias == "portOverflow" {
			return &LimitError{"aliases", 128}
		}
		rec := tracerecord.Record{Version: 1, Record: "step", Sequence: d.sequence + 1, Machine: "iotc", Instance: instance, MonoNS: core.Instant(delta), Input: summary}
		if err := d.append(rec); err != nil {
			return err
		}
		d.sequence++
	}
}

func udpPacket(data []byte) (flow, []byte, error) {
	// Decode only the three supported layers. In particular, UDP port numbers
	// cannot trigger arbitrary application dissectors on sensitive payloads.
	var eth layers.Ethernet
	var ip layers.IPv4
	var udp layers.UDP
	parser := gopacket.NewDecodingLayerParser(layers.LayerTypeEthernet, &eth, &ip, &udp)
	parser.IgnoreUnsupported = true
	var decoded []gopacket.LayerType
	if err := parser.DecodeLayers(data, &decoded); err != nil || parser.Truncated {
		return flow{}, nil, &DecodeError{Kind: MalformedCapture}
	}
	if len(decoded) != 3 || decoded[0] != layers.LayerTypeEthernet || decoded[1] != layers.LayerTypeIPv4 || decoded[2] != layers.LayerTypeUDP || ip.Flags&layers.IPv4MoreFragments != 0 || ip.FragOffset != 0 {
		return flow{}, nil, &DecodeError{Kind: UnsupportedLayer}
	}
	// The layer decoder exposes the version without validating it.
	if ip.Version != 4 {
		return flow{}, nil, &DecodeError{Kind: MalformedCapture}
	}
	if udp.Length < 8 || int(udp.Length) != len(ip.Payload) || len(udp.Payload) != int(udp.Length)-8 {
		return flow{}, nil, &DecodeError{Kind: MalformedCapture}
	}
	src, ok := netip.AddrFromSlice(ip.SrcIP)
	if !ok {
		return flow{}, nil, &DecodeError{Kind: MalformedCapture}
	}
	dst, ok := netip.AddrFromSlice(ip.DstIP)
	if !ok {
		return flow{}, nil, &DecodeError{Kind: MalformedCapture}
	}
	return flow{netip.AddrPortFrom(src.Unmap(), uint16(udp.SrcPort)), netip.AddrPortFrom(dst.Unmap(), uint16(udp.DstPort))}, udp.Payload, nil
}

// append reserves the actual worst-case trailer before admitting each event.
// Both event and trailer retain trace HTML/JS escaping and v1 omission tags.
func (d *decoder) append(r tracerecord.Record) error {
	b, _ := json.Marshal(r, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	trailer, _ := json.Marshal(tracerecord.Record{Version: 1, Record: "end", Sequence: r.Sequence + 1, Dropped: 1}, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	if len(b)+1+len(trailer)+1 > d.limits.OutputBytes-len(d.output) {
		return &LimitError{"output bytes", d.limits.OutputBytes}
	}
	d.output = append(d.output, b...)
	d.output = append(d.output, '\n')
	return nil
}

// finish uses the same trace encoding options as append for its loss trailer.
func (d *decoder) finish(err error) (Result, error) {
	d.result.Incomplete = err != nil
	var lost uint64
	if err != nil {
		lost = 1
	}
	b, _ := json.Marshal(tracerecord.Record{Version: 1, Record: "end", Sequence: d.sequence + 1, Dropped: lost}, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	if len(b)+1 > d.limits.OutputBytes-len(d.output) {
		return Result{Incomplete: true}, &LimitError{"output bytes", d.limits.OutputBytes}
	}
	d.output = append(d.output, b...)
	d.output = append(d.output, '\n')
	d.result.JSONL = d.output
	return d.result, err
}
