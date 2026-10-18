# IOTC outbound client

This package implements the revision-29 extended-RID outbound client profile. It
is a pure protocol owner, not a network client. Its edge adapter must supply
the effects below. The synthetic fixtures do not establish vendor
interoperability.

## Edge contract

1. Verify the [license envelope](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/sdk-licence.md#licence-envelope) with
   `ParseLicenseEnvelope`, the raw RSA operation with `LicensePublicKeyPEM`,
   `UnmaskLicense` and a constant-time digest comparison before calling `ParseLicenseJSON` and `New`.
2. Allocate a new nonzero local Attempt for every dial, and a fresh machine.
   Generate fresh [attempt material](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#cryptographic-primitives)
   with crypto/rand. For every Generate, call `edge.HelloSerialStart` with the
   injected RNG (`crypto/rand.Reader` in production), and supply its independent
   nonzero uint32 in `Material.HelloSerial`. The edge must supply distinct nonzero
   master and P2P nonces; equal values fail local material admission.
   Propagate RNG failure as EffectFailed; never substitute a counter or old seed. Never recycle any
   previous attempt's crypto context. Discovery completions echo the Generate/Resolve
   Token as well as Attempt. `MaterialReady` supplies the public slot92
   (DER91 + spare byte), master material and opaque crypto handles.
3. Resolve the license-derived hostname, returning at most four IPv4 addresses.
   Open UDP ownership for the attempt; execute `OpenTCP` with its exact Link ID.
   `Connected`/`LinkFailed` must carry that ID. Ports 80/443 are [raw protocol TCP](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#endpoint-discovery).
4. Execute `Seal` and `Derive` using the [master and server crypto primitives](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#crypto-primitives)
   and [server key agreement](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#server-key-agreement).
   `RawMasterRSA` receives an already right-zero-padded 256-byte input from the
   machine and uses `MasterPublicKeyPEM`; these shipped public constants are
   also retained in `testdata`. Return an attempt-scoped Key handle and IV;
   enforce the record encoder's material checks.
5. `Open` is an authenticated GCM operation. Return Decrypted only after tag
   verification, echoing the one-shot Token and Attempt. Authentication failure
   returns CryptoRejected; operational failure returns EffectFailed with the
   action's exact Token and Link. Retired tokens are ignored. Never pass
   unauthenticated plaintext to the owner.
6. Deliver each UDP datagram as one Wire event. For TCP, use one Decoder per
   connection, feed at most 8192 bytes at a time, and route each returned record
   separately. Framing errors close that TCP connection; no arbitrary socket
   read is treated as a record. Wire Marker is the locally sampled timeval used
   when installing master-list ECC metadata, with nonzero Seconds.
7. `Send` owns a complete packet. UDP preserves its boundary; TCP writes every
   byte in order, retaining partial writes in a bounded queue. Send rejection
   becomes LinkFailed with the exact Link (including TCP generation). A stopped
   executor uses the explicit AttemptFailed event; it must not relabel a late
   operation failure as attempt-wide. Upper layers offer Write only with edge queue credit;
   this machine retains no application queue. Write is one DTLS datagram, 1–1388
   bytes. Received is one datatype-1, channel-0 payload for DTLS. IOTC Ready is
   only transport readiness, never DTLS authentication or tunnel readiness.
8. Replace a single owner timer with ArmTimer; cancel on CancelTimer. All events
   are serialized. CloseLink cancels that endpoint's pending dial/read/write
   effects. UDP endpoint retirement must not close the attempt's shared UDP
   socket while another endpoint or the selected peer path still owns it. Disposed cancels **all** attempt effects and releases every socket,
   key and private-key handle, including resources created by late callbacks.
   Cancellation never waits for network I/O. The edge must retain an attempt
   tombstone until its workers finish; no new attempt may adopt old handles.

The edge implements the [rendezvous key/IV cache contract](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#gcm-key-iv-and-cache-contract)
within each attempt; replacement attempts never reuse its crypto context.
Neither actions nor configuration should be formatted into logs; errors are
fixed, redacted strings.

Bounded receive framing preserves unknown modern record types and marks unknown
inner controls explicitly, following the [unknown-record policy](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#record-types)
and [relay admission rules](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/relay.md#relay-result-codes). Unsupported device-role
and integrity-only modes remain disabled. Unknown relay results leave the local
60-second attempt deadline in force. Authenticated unknown controls never
authorize a path or refresh liveness. [Alert triggering types](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/relay.md#server-alerts) remain diagnostic.
Receive routes remain opaque except for the knock refusal bit; send builders retain canonical bytes under the
[legacy-header policy](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#revision-and-flag-bits-on-receive).

See [receive diagnostics](../../docs/reference.md#transport-telemetry) and [JSON input policy](../../docs/reference.md#json-file-compatibility).

## Scope and local policy

The production edge draws random eight-byte RIDs and reserves each nonzero RID
in process-wide history before returning attempt material. History survives
worker disposal, close, refusal and the 20-second refusal window; simultaneous
workers cannot reserve the same RID. Entropy failure or 32 unsuccessful draws
fails generation. History retains at most 16,777,216 reservations, including
attempts whose later crypto setup fails. At that internal ceiling, generation
returns a sanitized `RIDHistoryExhaustedError` (also classified as `ErrIOTCCrypto`)
and the executor logs `iotc_rid_history_exhausted` with restart guidance. The
attempt follows ordinary session setup failure and cleanup; no reservation is
evicted or reused. This lazily allocated count budget allows about eight years
at one attempt per 15 seconds; it is not configurable from the CLI. Restarting
the process clears history and restores the budget. Restarts have no shared
history and random collisions across processes remain possible.

This process-lifetime RID reservation is local policy beyond the
[native RID generation](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/session.md#identity) and [recent-RID guidance](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#attempt-and-cache-lifetimes). The edge has no receive
filter based on closed RID history: current attempt, crypto, UID/RID, source and
state correlation govern acceptance. LAN-search prechecks remain outside this
profile.

Discovery uses modern master records and [parallel UDP direct/relay work with TCP fallback](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udptcp-race).
TCP ports cycle on failed opens. Repeated knocks and repeated authenticated punch
instructions on a retained direct path support ACK loss.
Local [hello admission](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#receive-catalogue) requires the serial/instance form and a retained echo match,
while tolerating trailing bytes. The owner enforces the
[authenticated receive checks](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#receive-processing) before state updates and retains the
selected TCP relay connection. Within a selector interval, a completed direct
handshake takes priority over a pending relay path.

Resource limits are local policy: four DNS masters, 32 endpoint slots, twelve
address-key cache entries, twelve peer candidates (eight WAN + four LAN), 64
pending crypto operations, one in-flight decrypt per endpoint, one seal per
endpoint/opcode, 1416-byte packet, 8192-byte stream input. Decoder retains a fixed
1416-byte array. The machine retains no event buffers, ciphertext, plaintext or
application queue; pending operations retain only fixed metadata/AAD. Adapter
queues and outstanding crypto output need their own count and byte budgets.

The connecting-only relay handler implements [relay-info selection](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/relay.md#record-relay-messages-client-sdk);
the already-connected response branch is not implemented. Real backend
conditions for 030e remain unknown.
The sequential-selector profile remains unimplemented. Production leaves the optional P2P port count disabled.

The total attempt deadline is a conservative 60 seconds, even though the native
SDK can spend that long in individual stages. Monotonic timers approximate the
native scheduler's counter gates; they do not reproduce its wall-clock quirks.
The owner implements [session liveness](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/session.md#session-status-and-liveness)
with bounded catch-up on clock jumps and disposes immediately after the
[close exchange](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/session.md#close).

Deferred optional paths: short RID/historical peer profiles, legacy master and
precheck exchange, device registration/sleep/server role, raw device NAT keepalive,
LAN broadcast search and unauthenticated LAN-direct bypass, requested IOTC ports
and optional eight-byte IOTC authentication, telemetry, type5 registration
keepalive and unsupported HMAC types16–23. [Try-port traversal](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#try-port-traversal) is also deferred; ordinary candidate punching and
relay/TCP fallback provide the minimal path. IPv6/NAT64 has no established wire
interpretation. Deferred traversal means some networks that could be direct
using that algorithm will use a relay instead.

Fixtures retain their original labels: synthetic structural/offset vectors, not
captures or evidence of successful vendor interoperability. The simulator's
server is a protocol-shaped test peer, not a claim about server deployment.

`Failed.Failures` is a bounded bit set of terminal path reasons. A refusal on one
server leaves other servers and direct candidates usable. When no alternative
remains, failure is immediate; at the total deadline, known reasons accompany
ConnectionTimeout. AnotherRelayRequest is the wire -4 result, not proof of the
cloud-level “device in use” condition. [Alternate-address NAT observations](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#nat-classification) are an
inferred conservative policy: extended replies must echo a retained hello serial
and instance, and the source IPv4 must appear in the master-advertised list.

Direct and relay negotiation have independent lifetimes after UDP precheck.
Relay acceptance and refusal preserve the server's authenticated punch reception
and independently scheduled direct retries. A server completing precheck after
transport selection joins rendezvous while the attempt remains live. Ready relay allocations remain bound to their individual endpoints;
selection rescans healthy alternatives if its preferred endpoint fails.

See [direct candidate ownership](../../DESIGN.md#iotc-path-ownership-invariant).

TestPathProgressSeeds drives ArmTimer through retry boundaries and the total
attempt deadline, observing actual request Sends from mixed transports, late
precheck and replaced TCP generations. Its bounded mock edge randomizes each
crypto completion; loss/recovery tests also exercise real GCM.
TestPathOwnershipSeeds covers short outcome permutations and post-Ready recovery,
including individual candidate and effect outcomes. These complementary checks
are bounded models, not an exhaustive protocol or deployment model.
