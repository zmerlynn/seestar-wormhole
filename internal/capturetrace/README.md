# Offline capture projection

This internal package supports classic pcap, Ethernet II,
nonfragmented IPv4, UDP, and [incoming modern IOTC type-1 master replies](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#master-query-record-type-1). It reads
the explicit attempt's master reference (3) from this repository's `IOTC_KEY`
keylog extension, authenticates AES-128-GCM, then calls `iotc.ParsePacket`.
The existing `iotc.ParseRecord` owns envelope framing. The Go standard library
owns GCM; gopacket owns capture, Ethernet, IP and UDP formats.

Decoding uses a single vendor envelope and a saved master pair without
handshake reconstruction, stream reassembly, an ECDH cache or inferred endpoint
selection. pcapng, other link types, IPv6, fragmented IP, other IOTC record types,
legacy scrambling, DTLS, RDT and tunnel reassembly are unsupported. This is an
internal adapter with no CLI command; the executable exposes proxy/pull.

`Decode(capture, keylog, Options{Attempt: ...})` returns JSONL accepted directly
by `internal/tracediff.Compare`. Each authenticated inner frame becomes an IOTC
wire **observation**, with its opcode, length, source peer/port aliases, UDP
transport, directional-flow instance and relative capture time. There are no
inferred state-machine completions, tokens, timers, accepted identities or
output actions. Flow instance numbers are local counters. Source IP and port
aliases use the exact recorder projector; destination identities are used only
in bounded private flow keys and are never serialized. Capture order is retained.
Authentication proves possession of the saved key, not runtime acceptance of a
nonce, UID, SID or protocol state.

Complete output means complete decoding of this narrow wire observation scope.
Use a corresponding wire-only recording for equality comparisons, such as the
synthetic recording in the tests. Internal machine observations are unavailable;
when comparing a runtime recording, declare its unobserved machines in
`tracediff.Options.RightUnavailable` (or `LeftUnavailable` if appropriate).
IOTC decisions/actions also remain unavailable: declare `iotc` unavailable for
full-runtime comparisons. See [projection equality limits](../tracediff/README.md).

Every failed/unsupported/truncated observation ends decoding and returns a
sanitized typed error plus an incomplete projection. Its end record has
`dropped: 1`, preventing tracediff from reporting EQUAL even if the caller ignores
the error. Earlier authenticated observations are preserved. No substitute event
is emitted. If the output bound cannot hold even a trailer, JSONL is empty and
must not be compared. Invalid options likewise return no projection.

Bounds cover capture bytes, expanded packet bytes, packets, directional flows,
authenticated records, encoded output and keylog bytes. pcapgo's advertised
snaplen is clamped before any packet allocation. Alias storage inherits the
recorder's 128-peer/128-port limits. All fixtures are generated in memory using
obviously synthetic material. Inputs, decrypted buffers and keylog bytes are
never logged, written to files or included in errors; callers still own their
sensitive input readers. This package performs no capture acquisition or deletion.
