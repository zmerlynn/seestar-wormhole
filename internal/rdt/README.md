# RDT owner contract

`New(DefaultConfig())` creates one single-owner machine for an authenticated,
packet-preserving transport. Supply the local [connection identity](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#connection-identity)
allocated by the session and the previous remote ID when reusing a channel.
Read `RemoteID()` before discarding the instance. Each new attempt needs a new
machine; the session must route events with its own attempt stamp. RDT adds no attempt fields to the wire.

See [machine clock and buffer ownership](../../DESIGN.md#layering-and-ownership).

1. Deliver `Start` after DTLS readiness. Deliver decoded authenticated `Packet`s
   with `Receive`. Both codec validation and machine validation remain bounded.
   `Ready` follows the [creation exchange](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#creation) and must precede application writes.
2. Execute each `Send` as one atomic packet write. The machine treats the action
   as a successful bounded adapter handoff, timestamps that handoff, and retains
   DATA for acknowledgment. Datagram loss is normal. Adapter rejection or
   nonpositive/partial transport writes require `TransportFailed`; there is no
   split-packet continuation. Do not accumulate an unbounded action queue.
3. `Write` returns `Accepted{Token, Count}`, accepting a prefix of at most 8196
   bytes and available byte/packet capacity. Acceptance is local, not delivery.
   `WriteUrgent` accepts at most one configured payload and shares stream order.
   Retry an unaccepted suffix using `WriteCredit`; no empty-write busy loop.
   `Credit()` reports available queue capacity, not a wire receive window.
4. `Readable` announces contiguous bytes. `Read{Count, Token}` requests 1–8196
   bytes and returns at most that many in `Bytes`. No pending read is retained.
   A partial drain reannounces readability; when empty, wait for `Readable`.
   `QueueStatus.Usage.RXBytes` includes out-of-order and unread bytes: route this
   to the tunnel's shared RDT queue-size input. A quiet slow reader may retain
   its bounded backlog indefinitely.
5. Replace the owner's sole timer on `ArmTimer`, delivering `Tick` at its exact
   deadline, and remove it on `CancelTimer`. `sim.Timer` is the tested adapter.
   Stale callbacks are harmless. All deadlines use monotonic time, including
   ACK aggregation and RTT samples; wall-clock changes cannot expire a session.
6. `Close` stops writes and queues FIN after all DATA acknowledgments. `Closed`
   terminates network ownership; unread contiguous bytes remain available via
   `Read`. `End` follows the last bytes exactly once. `Failed` clears both queues.
   `LocalAbort` and creation-time `Cancel` implement the
   [abort and exit exchanges](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#close-abort-and-failure). Never wait on a blocked
   consumer to stop network work.

## Wire behavior and deliberate local policy

Literal ACK and heartbeat fixtures independently encode the wire layouts used
by this package; the local policy and integration limits below describe the
supported profile.

The machine implements [sequence ordering](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#sequence-numbers), [ACK processing](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#ack-processing-at-the-sender),
[bounded SACK decoding](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#decoding-rules-for-new-implementations) and [connection-ID admission](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#connection-identity).
Arbitrarily late zero-ID traffic and repeated ID reuse remain ambiguous; the
implementation adds no wire generation.

Default bounds are 1 MiB/1024 DATA packets sent and 4 MiB/4096 received, plus one
FIN control slot. Configurations are capped at 64 MiB/65536 packets per direction.
A write can allocate at most 8196 payload bytes. A read can return at most 8196.
Payload capacity equals retained length, including after a partial read. Metadata
capacity is a bounded slice-growth factor of packet limits; released slots are
zeroed. No output-action queue is retained. With positive SendCapacity, deferred
unreliable controls are coalesced by packet type until SendComplete returns
handoff capacity. DATA and FIN remain in the transmit queue. A step can
emit a bounded sweep of up to the local window (ceiling 6500) datagrams, plus a
small constant number of controls/status effects. The composing owner must budget
these returned datagrams and its inbox separately. Eight-hour tests measure live
ownership and simulator queue peaks, not whole-process RSS or full-stack device
interoperability.

The following local policies cover shutdown, receive pressure and retransmission:

- **FIN:** no interpretation of the [mixed-unit native FIN expression](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#fin-retransmission) is claimed.
  `CloseTimeout` defaults to a separate five-second local deadline starting at
  `Close`, covering DATA drain and FIN even if heartbeats continue. Expiry sends
  QUIT, clears memory and reports `local close timeout`. RTO retries run inside
  that bound. This is configurable local shutdown policy, never `4 × RTO`.
- Receive capacity is checked before acknowledging new bytes. Saturation rejects
  the datagram without ACK and emits `ReceivePressure`; the sender retains it for
  retry. The next missing contiguous packet reserves one slot and up to 1280 bytes,
  even behind a short/partially consumed readable prefix. Later selectively ACKed
  packets cannot consume the room needed after that prefix drains. A packet must
  still fit the configured RXBytes to be receivable.
  RDT provides [no advertised receive window](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#receiving-and-reading). This bounded
  loss policy can reduce throughput and delay every multiplexed channel during
  reassembly; it never evicts acknowledged bytes or fails the session on capacity.
  The upper tunnel must continue draining and pause individual socket producers.
- Under the [sender scheduling permission](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#window-and-pacing), normal initial sends obey the
  numeric window without the append-path allowance; adapter handoff is immediate
  and omits the blocking-write adjustment. The machine implements the linked
  window arithmetic and [retransmission policy](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#retransmission).
- Urgent retries use a local minimum 20ms interval to avoid a zero-time loop.
- A write flushes its accepted tail immediately; [ACK fallback](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#ack-generation) is scheduled
  100ms after pending work rather than worker polling.
- Packet fields are validated even if callers construct `Packet` directly,
  including [identity](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#connection-identity) and [acknowledgment](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#ack-processing-at-the-sender) checks before queue release.
  Unsent DATA cannot be released by an ACK; FIN requires its own sentinel ACK
  while in flight.
- Stale previous remote HEARTBIT cannot establish a fresh connection.
- [RTT estimation](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#rtt-and-rto) retains a separate retransmitted flag, so an 8-bit attempt-count
  wrap never makes an ambiguous RTT sample eligible again. The transmitted byte
  still wraps. Receive close with a queued sequence gap fails instead of emitting
  false EOF; orderly close preserves contiguous unread bytes. Standalone QUIT
  fails even with an empty or contiguous receive queue: a lost tail is invisible
  there. A QUIT arriving after completed FIN/FIN_ACK closure is ignored.

A stopped instance is terminal. It does not keep a FIN_ACK linger responder alive
for duplicate FIN; the emitted QUIT also tells the peer to stop. If both replies
are lost, the peer's own close bound/watchdog governs cleanup. Session integration
must distinguish `Failed` from ordered EOF and release transport resources on both.
