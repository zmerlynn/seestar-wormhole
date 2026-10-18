# Design

This repository contains a Go 1.27 implementation based on the [protocol
specification](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/00-overview.md#protocol-stack). The [developer guide](docs/development/README.md#test-layout-and-evidence)
describes its sources and their limitations. The composed proxy implements the cloud-to-tunnel
runtime and [internal stream interface](#internal-stream-interface).
Tested devices, phones and platforms are listed in [status](docs/status.md);
they do not establish complete interoperability. Downloads use standard SMB clients over a proxy listener.
The proxy does not interpret SMB.

## Layering and ownership

```text
cmd/seestar-wormhole -> proxy -> session -> cloud (allocation/auth/stop via HTTPS edge)
                             -> iotc (discovery, rendezvous, direct/relay)
                             -> dtls (authenticated packet adapter at edge)
                             -> rdt (packet reliability -> ordered byte stream)
                             -> p2ptunnel (multiplexed local TCP channels)
                                    |
                               edge TCP workers
```

`core.Machine[E,A].Step(event, now) []A` is the composition contract. Each owner
serializes all calls, supplies monotonic elapsed nanoseconds, and executes returned
actions through bounded adapters. Core/machine code has no goroutines, locks,
sockets, wall-clock access, sleeping, or hidden timer work. The session owner samples its monotonic epoch; wall time for UTC millisecond
logging is separate. Tunnel
clamps regressing injected time. New session attempts get new machine instances.

Events borrow their buffers only for Step. Machines copy retained bytes; actions
own their output bytes. Adapters transfer or copy those buffers and never mutate
machine storage. Local completions carry a channel generation and action token;
the session runtime must additionally attach the originating Attempt before routing
a completion, so an old allocation cannot affect a replacement session. These
identifiers never appear in the [tunnel frame envelope](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#frame-envelope).

The composed runtime routes cloud completion -> IOTC -> DTLS readiness -> RDT
readiness -> tunnel Start, in that order. Tunnel Send means enqueue ordered bytes
to RDT and return `Sent{Token,Count}` after local acceptance, not remote delivery.
Partial acceptance preserves the suffix; zero progress waits for
TransportWritable, and terminal failure stops the tunnel. Each tunnel frame is
queued whole and never interleaved with another frame's partial continuation.
The runtime preserves each complete RDT packet per DTLS application write. [IOTC and
DTLS preserve datagrams below RDT](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#packet-boundary); loss, duplication and reordering are handled
by the RDT machine, never by the tunnel parser.

The concrete runtime is in `internal/proxy`, `internal/session/sessionedge`, `internal/edge/iotcedge` and
`edge.SecureDTLS`. The session owner serializes Step calls, bounds its inbox and
workers, replaces timers and joins cleanup before replacement. `SecureDTLS`
owns the maintained library's handshake deadline, workers and authenticated
profile check. Independent crypto/interop and loss/MTU tests exercise that edge.
[No fallback to plaintext is permitted.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#secure-channel)

## Tunnel contract

Construct `p2ptunnel.New(DefaultConfig())`, then deliver Start only after secure
RDT creation. Stream events contain at most 8196 ordered bytes. Decoder buffers
one header/body and handles arbitrary splitting/coalescing, including phase
changes after login. The codecs cover the [RDT message catalog](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#message-catalogue)
and phase-dependent [tunnel message catalog](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#message-catalogue). The policy is
strict-send/permissive-receive: send encoders reject unknown operations and emit
canonical layouts. Unknown-message handling follows the [RDT decoding rules](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#decoding-rules-for-new-implementations)
and [tunnel message rules](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#message-catalogue), after validating
complete declared lengths and receive caps. Unknown login commands remain
rejected. Invalid lengths, unterminated service fields, reversed active SACK
ranges and invalid SACK counts are rejected before unbounded allocation or
operational use.
Authentication and correlation checks remain required. Receive frames/packets
preserve opaque fields; typed builders and raw sends enforce the canonical
[IOTC header](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#revision-and-flag-bits-on-receive), [SETUP](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#setup-01) and
[verification field](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#port-and-service-verification) policies.

Agent Open accepts a port, mapping index and opaque request ID and performs
[advertised port verification](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#agent-behaviour) before SETUP. Opened reports local SETUP
queueing, so a later [peer rejection](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#setup-01) can still fail the connection.
The adapter must retain the local socket until Opened or OpenFailed, and
cancel it on either later CloseLocal or session failure. No free slot reports temporary local capacity. The composition retains at most
128 pending/active clients and retries pending admissions once per second until
the original wire quarantine permits reuse or the dial context is canceled.

The allocator follows [channel scanning and quarantine](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#channel-allocation-and-reuse)
using the injected Start epoch for its sweeps.
A fresh local generation is assigned on every activation. Incoming server SETUP
can replace an active/quarantined slot; a stale local completion cannot change it.
There is no wire generation to distinguish arbitrarily delayed bytes after reuse.

The Server role exists for simulation/embedding. It allows only configured
loopback ports; it emits an asynchronous ConnectLocal action. Connected is a
local completion, never a positive wire ACK. Service verification replies invalid;
nonempty service SETUP is rejected. Unlike the [native Server login timeout](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#sequence),
the simulator defaults to 30 seconds; embeddings should configure 90 seconds
for Server parity. See [Not implemented](#not-implemented) for unsupported paths.

ReadReady requests a local read opportunity. ReadLocal reserves an 8196-byte
outbound frame and slot until matching LocalData/LocalEOF arrives. Empty LocalData
means transient would-block, consuming a one-shot read opportunity. The machine
implements the [peer read-state controls](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#receiving); the frequency of racing reads after
PAUSE remains unresolved.

Received DATA enters a bounded local-write queue. WriteLocal exposes at most 8192
bytes; LocalWritten reports partial progress. Zero progress waits for
LocalWritable. A temporary EINTR/would-block uses zero progress; terminal failure
uses LocalWriteFailed. The adapter implements [half-close and abortive close](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#state-flags)
through ShutdownWrite and CloseLocal, preserving the queue-drain obligation.
LocalClose sends CLOSE result 1 and releases the channel. Transport failure
releases every channel without waiting on a blocked writer.

Flow control uses local fair attribution in place of the
[native shared-backlog formula](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#sending) to keep bulk traffic from pausing an empty RPC. Here a channel retaining `C` bytes receives
`floor(sharedRDT * C / totalTunnelRetained)` shared-pressure bytes. Its `Q` is
`C` plus that share. An empty channel has `Q=0`, including when every channel is
empty. The owner continues draining RDT independently of local consumers.
RDT's unread/out-of-order bytes cannot yet be demultiplexed; this proportional
estimate is local overload policy, not recovered native accounting, exact wire
ownership or negotiated per-channel credit. Rounding leaves less than one byte
per nonempty holder unattributed. No payload storage or reservation changes.

Shared-count updates and retained-queue changes (DATA, local progress, write
failure, release or reclamation) recheck all active channels in a bounded
256-slot scan once per Step. A holder draining to empty resumes even while RDT
remains busy; changing one holder's share also rechecks its siblings. Flow controls use one pending state per channel and one immutable in-flight
frame, outside the DATA TX budget (at most 257 four-byte controls). Unsent
PAUSE/RESUME pairs cancel, repeated WAKE opportunities coalesce, and RESUME
supersedes pending WAKE. A zero RDT completion allows reconsidering the handoff;
a partial completion must finish the original frame before any other frame.
A WAKE generated while PAUSE is outstanding remains pending until that PAUSE
is completely accepted. Selecting or retrying PAUSE cannot consume its WAKE;
selecting WAKE consumes that opportunity. A later desired PAUSE/RESUME transition
or channel release supersedes pending WAKE; a zero-accepted WAKE is restored
only if the same channel generation is still paused and no intervening desired
PAUSE/RESUME transition superseded that opportunity. A new opportunity generated
during an outstanding WAKE is independently retained and coalesced.

Control selection rotates across all 256 wire channel slots. When both classes
are ready, complete controls alternate with FIFO DATA/lifecycle frames. Assuming
RDT accepts offered frames, a continuously pending control is serviced within
512 complete frames after an already outstanding control (513 including it);
the kth queued
FIFO frame completes within 2*(k+1) frames. No channel can monopolize accepted
handoffs. These are local service bounds, not wall-clock or remote-delivery
bounds: zero acceptance waits for transport credit, and a partial frame always
finishes before selecting another frame.
The machine applies the [PAUSE/RESUME hysteresis and WAKE trigger](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#sending)
to the attributed `Q`. Configurations reject B < 2 so integer low water is
positive. No extra hysteresis timer is added. The runtime polls read eligibility on
socket readiness / queue-credit changes. It must not turn lack of a ReadLocal
action into a busy retry loop. RDT producer backpressure is explicit completion
and reserved capacity, not a sleep in a protocol layer.

Local receive storage allows for the [backlog after PAUSE](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#backlog-after-pause):
`p2ptunnel.DefaultChannelBytes` allows 16 MiB per channel on demand, with a
64 MiB aggregate ceiling. B is configurable, defaulting to 4 MiB as selected
by the synthetic buffer sweep; low water follows the linked flow rule.
The envelope budgets a rounded 4 MiB of device RDT send backlog, one MiB of
tunnel staging and a final 8192-byte read. The native
[socket-read throttle](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#sending) and [RDT write pressure](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#writing) do not prove a hard burst cap.
At the proxy maximum B=8 MiB, the allowance uses about 13.01 MiB, leaving
almost 3 MiB extra channel headroom. Four maximally retained channels fit the
64 MiB aggregate. These are conservative local bounds, not negotiated credit
or a proven maximum PAUSE overshoot.

There is no active-channel idle/close deadline. A stuck local reader pauses its
own socket producer while the owner continues draining RDT and serving siblings.
A channel exceeding its own envelope gets CLOSE result 1. Aggregate pressure
first reclaims holders with no successful local-write completion for at least
one second. The pure core uses injected owner time, observing only valid,
positive-count completions for the outstanding generation/token. Partial writes
count; zero writes, readiness notifications, stale callbacks and further DATA do
not renew progress. Each empty-to-nonempty receive queue starts a fresh grace,
so idle time cannot make a new transfer look stalled. This one-second grace is
local overload policy: an 8192-byte delivery at the accepted 64 KiB/s completes
every 125 ms, allowing eight delivery intervals before classifying it stalled.
It is consulted only on aggregate pressure, with no idle-close timer.

Within the stalled class, reclaim the largest retained queue first, ties by
lowest channel ID. Reclaim more stalled holders if needed before considering
any progressing holder. If every remaining holder is within grace, all have the
same fair share (aggregate ceiling divided by the number of nonempty holders).
Shed the holder furthest above its share, equivalently the largest retained
queue, ties by lowest ID. Newly started writes receive that same bounded grace;
there is no indefinite exemption for in-flight I/O. This fallback frees the most
capacity per close and bounds admission work when every client is progressing
but finite storage still cannot hold their combined already-enqueued bursts.
It does not promise arbitrary concurrent bursts will complete.

Incoming DATA is not included in victim selection. Empty recipients cannot be
victims; smaller existing RPC queues share the same progress-aware protection
as bulk queues, with no port priority. An admitted transfer that continues
completing writes within the grace survives whenever stalled holders can free
the needed capacity, regardless of its backlog size. Per-channel overflow still
closes that channel directly. Custom smaller envelopes use the same policy.

Reclamation repeats only until the incoming frame fits; at most 256 positive
backlogs are released, each found by a fixed 256-slot scan. Channel overflow is
checked first. A victim's in-flight local write is canceled by the edge, and
stale completions cannot change accounting. Both byte ceilings remain enforced;
there is no waiting for a stuck reader and the shared RDT drain continues.
Already-ACKed RDT bytes for live channels are never dropped.
RDT saturation instead rejects new packets without ACK, preserving retry and a
reserved missing-head slot. Loss/reassembly can delay the entire ordered RDT
stream; the protocol has no per-channel reliability stream or peer receive window
that eliminates this transport head-of-line delay.

Literal capacity fixtures use a 6/8 MiB receive profile and 64 KiB threshold;
additional tests exercise a 9 MiB admitted burst and stalled-holder precedence
at the 64 MiB aggregate.

Capacity regressions transfer and hash-check 16,594,560 bytes with a saved 4 MiB
burst and delayed socket completions. Composed tests use four pipelined one-MiB
opaque replies, a 64 KiB/s reader, a stuck reader with 36 sibling RPCs over 180
virtual seconds, and a standard-library HTTP GET of the same file size. Mixed-reader regressions
also verify a full literal-response SHA-256 transfer under aggregate pressure,
progressing RPC against 127 stalled holders, and eight healthy reconnects against
reintroduced stalled pressure. Grace boundaries, partial/zero writes, stale
callbacks, fresh backlogs after idle, and multi-holder reclamation are exercised. A
synthetic missing DATA followed by 700 packets reproduces RDT pressure under a
bounded 12.8 MB/s simulated link. This models a failure shape rather than
reconstructing captured packet ordering.
A separate paced-transfer regression exercises slow delivery.

## Resource bounds

Defaults are local policy, not vendor-negotiated limits:

| Resource | Limit / accounting |
|---|---|
| Active channels / mapping cursors | 256 / fixed 256 |
| Retained per-channel receive payload | 16 MiB (allocated on demand) |
| Retained session tunnel receive payload | 64 MiB |
| High / low flow threshold | Default B=4 MiB / floor(4B/5); proxy flag 2 bytes..8 MiB |
| Outbound retained frames plus read reservations | 1 MiB, 512 frames |
| Reserved read | 8196 bytes and one frame per outstanding ReadLocal |
| Lifecycle-control headroom for ordinary reads | 64 bytes, four frame slots |
| Separate flow-control reserve | 257 frames / 1028 bytes; fixed per-channel state plus in-flight frame |
| In-flight local writes | One copy of <=8192 bytes per active channel |
| In-flight RDT write | One copy of <=8196 bytes |
| Decoder storage / stream event | Fixed 8196 bytes / <=8196 bytes |
| Port verification | One request; five seconds |
| Runtime owner timer | One replaced deadline (login/verification/earliest quarantine) |

Receive queues pack bytes into exact-sized chunks of at most 8192 bytes. Only
the first and last chunks can be partial; appending or partially consuming copies
at most one chunk, and a delivery copies at most 8192 bytes. Consumed slots are
zeroed and an empty queue releases its slice. The live chunk count is bounded by retained
payload/8192 plus two chunks per active channel, including one-byte DATA frames.
Backing metadata arrays can retain each channel's historical peak until empty;
their allocation is bounded by ChannelBytes/8192 plus two entries per channel,
with a constant allocator growth factor. The runtime layer reservation includes
this bound across all 128 channels, rather than charging only current payload.
`Usage` reports live payload/capacity/count, not RSS or Go allocator overhead.
Transient replacement adds at most one chunk; GC may retain dead allocations
until collection. Outbound frames remain fully charged until accepted in full.
Completed queue slots are zeroed so backing arrays retain no payload pointers.
Outbound metadata has a bounded constant growth factor relative to TXFrames;
channel and mapping tables are fixed. Server's allow-list is capped at 65536
ports (128 KiB), copied at construction.

Thus live owner payload is bounded by RX + TX/read reservation + parser, and
adapter copies by at most 256*8192 + 8196 bytes, plus bounded inbox buffers. The
runtime reserves budgets for its own queues and DTLS/RDT storage and sums all
layer budgets; these are not claims of a completed process-wide memory limit. Step
work is bounded by one <=8196-byte stream event (at most 2049 minimum-size frames),
a fixed 256-slot scan, and bounded per-frame queue copies. Result actions are
bounded by that input and at most one outstanding write/read per channel.

Receive overflow closes only the offending channel with result 1. Failure to
queue required outbound control stops the session rather than dropping data or
violating a budget. [These stricter policies are local resource limits](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/session.md#timers-and-limits), not
negotiated peer credit.
`core.Budget` and the simulator enforce both count and byte budgets, including
zero-length events. Tests assert live counters and capacities.

## Simulator and evidence

`sim.Scheduler[E]` uses a heap ordered by virtual deadline and insertion serial.
It rejects scheduling in the past, accounts queued bytes/count, and limits steps
per Run to catch zero-time loops. Wall time can jump independently in either
direction. `sim.Network` uses PCG with an explicit seed, independent PPM loss and
duplication, delay/jitter reordering, MTU drops, per-destination consumer pacing,
and bounded endpoints. It copies network input and atomically rejects sends that
cannot fit their duplicate copies. Transit arrivals are scheduled independently. On arrival, a packet reserves its
destination service time in arrival order; waiting service events form bounded
destination queues within the shared heap. Transit and waiting packets share the
same aggregate byte/count budget, with exactly one charged slot per packet.
Slow-consumer pacing delays delivery without blocking other destinations.
`NetworkScheduler.Run` counts both arrival and service events toward its step
limit. An arrival exactly at a pending service deadline joins behind that service,
even when its own consumer delay is zero. A conservative per-destination horizon
preflights time overflow atomically; it does not determine delivery ordering. This is a datagram model, not an invented reliable
stream. No network access or real-time sleep is used in simulation.

`sim.Timer[E]` translates a single owner deadline into a zero-payload scheduler
event. Arm replaces and Cancel physically removes the previous event, so stale
heap entries do not accumulate. Timer lookup/removal scans at most the scheduler
event limit. Paired and eight-hour tunnel harnesses honor ArmTimer/CancelTimer;
silent-peer tests assert exact login, verification and quarantine deadlines and
inject stale callbacks alongside normal input.

Tests cover deterministic seed replay, all impairments, parser fragmentation,
paired Agent/Server lifecycle, verified opens, flow thresholds, one-shot wake,
partial writes, both half-close orders, simultaneous END, quarantine, stale
callbacks, seeded lifecycle interleavings, teardown and resource exhaustion.
The accelerated eight-hour tunnel test keeps one writer blocked while 480 sibling
channels complete, with constant retained slow-channel memory. This checks
tunnel ownership accounting, not a whole-process RSS plateau or a full-stack
device soak. Separate RDT simulator tests exercise loss, reordering, duplication
and MTU drops.

Each implemented network parser is covered by Go fuzzing: RDT packet + SACK,
tunnel frame + typed payloads, and incremental tunnel stream framing. Seeds
include all message kinds and literal byte examples. `internal/rdt/testdata` and
`internal/p2ptunnel/testdata` retain the exact linked `layer-example.json`; it is a
structural synthetic fixture, not a device capture. Literal extra fixtures are
independent transcriptions of protocol field tables. See the
[developer guide](docs/development/README.md#test-layout-and-evidence) for fixture origins and limitations.

## Session ownership implementation

`session` provides the pure ownership machine and `internal/session/sessionedge` the
bounded single-owner runner over consumer-defined layer interfaces. Eager/lazy
idle, bounded queued clients, bounded recent-release refusal retry,
attempt stamps, jittered retry and aggregate reservations are implemented and
simulator-tested. The runner retains lease ownership until cleanup completes and
joins before a replacement attempt. Its interfaces remain independent of
concrete protocol layers; `proxy` provides the adapters to those layers.

Transport terminal outcomes remain owned by their attempt through cancellation
and cleanup. The worker captures each returned transport's bounded Events queue,
including partial construction results. Close quiesces producers and preserves
queued outcomes; after all closes, the worker drains residual terminal notices
and sends one sanitized aggregate before Released. Uncertainty outranks busy.
Release detaches owner callbacks to suppress usage/admission effects, while the
worker retains those queues until cleanup. The eight-slot completion queue fits
five progress messages, a construction failure, a terminal aggregate and Released;
joining before replacement prevents retired queues from acquiring a new stamp.

The session manager must preserve these invariants after every event:

- An allocation refusal uses ordinary bounded jittered backoff within **90 seconds** after this owner's session ends
  (`session.OwnSessionRetryWindow`, default `RecentReleaseWindow`). Both release
  of a ready session and transport loss start the window; confirmed stop of an
  owned lease reanchors it at completion, including partial allocation failures.
  This is bounded local retry policy, [not a vendor cooldown.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/state-timers.md#timers)
  Refusals with no lease cannot start or extend the window. Uncertainty always
  overrides retry. A refusal outside this window, without a recent own-session
  end, is treated as another holder.
- Otherwise busy is sticky for the current attempt, even after dial timeout or
  during cleanup. An ambient proxy enables `RetryBusy`: eager cloud refusals
  retry indefinitely from the first dial with the existing capped equal jitter
  (0.5–1s initially, doubling to a 30s cap, then 15–30s). No client demand or
  prior readiness is needed. Lazy cloud busy accepts
  fresh client demand; its minimum dial interval survives canceled/expired
  demand. Stop and restart to resolve blocked ownership/cleanup; Shutdown is
  terminal. Stale generations cannot change ownership or policy.
- Backoff constrains every source of dial demand, including lazy client arrivals. Retry-not-before and exponential history survive an
  empty client queue. A retryable own-session refusal preserves pending admissions
  across cleanup; a single lazy caller can therefore recover without resubmitting.
  Cancellation, Shutdown and terminal cleanup errors still remove demand.
  Demand before expiry waits within the queue budget; expiry
  without lazy demand does not allocate.
- Uncertain remote allocation or release blocks replacement. Cleanup must finish
  before any new worker starts. An allocator that cannot hand back ownership must
  report unsafe local cleanup or an unknown/malformed allocation response as
  a nonretryable outcome. Stop the owner, resolve remote ownership and restart
  before trying again. Bounded cloud stop failure is an explicit exception: record abandonment,
  finish local teardown and join, then use the existing backoff before fresh
  detail/online-link. [Service allocation expiry/reuse is
  unresolved](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#allocation-online-link); abandoned is never accepted. The recovery deadline allows one
  additional DialTimeout join grace, after which a broken adapter is fatal.
  Shutdown still performs its final bounded stop.
- Every acquired allocation has exactly one stop invocation, by either the
  allocator before handoff or the attempt worker after handoff, never both.
  Every returned partial transport is closed exactly once, in reverse order.
  A failed cloud stop is recorded as release_abandoned during recovery; failed final
  stop or local cleanup is sanitized and surfaced on every exit path,
  including cancellation, input closure, failed output and recovered panic,
  independently of whether the notification sink still works.
- Admission requires a fully ready current attempt. Reservations, queued demand
  and completion buffers remain bounded. Under fair successful completions and
  delivered timers, an unblocked manager eventually admits a waiting client
  through ordinary backoff.

The seeded simulator checks safety after each event and bounded liveness under
explicit fair-success suffixes. Dial phases, commands, failures and release
completions run on independent schedules, including duplicate and stale events.
Runtime tests additionally check actual lease/transport identities and exit errors.

## Internal package integration contracts

| Package | Current contract |
|---|---|
| `internal/rdt` | Authenticated packets and monotonic events produce datagrams, ordered bytes, write credit, queue status and readiness/failure. The pure machine owns ordering, acknowledgments, retries and bounded send/receive queues. |
| `internal/iotc` | Discovery, rendezvous and direct/relay selection produce a bounded packet path to DTLS. The edge owns UDP/TCP, DNS and cryptographic effects. |
| `internal/dtls`, `internal/edge` | The vendored Pion adapter preserves packets and reports handshake readiness/failure and the negotiated profile. Authentication inputs remain ephemeral; plaintext fallback is forbidden. |
| `internal/cloud`, `internal/cloud/cloudhttp` | Bounded typed JSON decoding and the HTTPS edge supply allocation results and stop completion. The edge owns environment credentials and sanitizes errors/logs. |
| `internal/session` | The sole device-session owner routes attempt-stamped events, reserves layer budgets and joins cleanup before replacement. Eager cloud busy retries indefinitely; lazy mode retries on demand. |
| `internal/proxy`, `cmd/seestar-wormhole`, `internal/edge` | Each client owns a tunnel channel through the stream/half-close adapter. Workers and inboxes are bounded; blocked local sockets cannot block the owner or siblings. |

Simulator, parser-fuzzing and independent-library checks do not establish
complete device interoperability or overnight stability. Recorded device
measurements apply only to the configurations actually observed; see the
[status](docs/status.md).

## IOTC path ownership invariant

The attempt's single-owner Machine owns a bounded set of alternatives, not one
shared progress slot. Until selection succeeds, every
usable alternative must remain reachable by ingress and by the selector.
The ownership invariant is: **no input from a previous attempt can affect the
current one**. Local completions retain their originating Attempt stamp; network
input is checked against current on-wire authority, even when a UDP tuple is reused:

- Each attempt receives a fresh independent random hello serial origin from the
  edge's injected RNG (crypto/rand in production), alongside fresh
  [attempt identity and crypto material](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#cryptographic-primitives). Hello serials increment independently
  of local effect tokens; zero is a local unissued sentinel and is skipped at
  wrap. No narrower range is established by observations. The [wire instance](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#receive-catalogue) remains the low 16 bits of RecordSID and is not sufficient attempt isolation by itself.
  Randomization removes deterministic cross-attempt reuse; a finite 32-bit
  unauthenticated field cannot guarantee absence of chance collisions or forgery.
  [The protocol provides no absolute replay-proof hello identity.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#receive-catalogue)

- Each endpoint owns discovery/precheck and its transport generation. [Selecting
  UDP or TCP retires the other transport](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udptcp-race) once selection is complete. Failure
  of one endpoint retires only that generation and its effects; TCP port fallback
  creates a new generation. It does not retire other servers. A server that
  completes precheck at any time while the attempt is live joins rendezvous
  exactly once when its transport is selected, including after another server
  has entered rendezvous or failed.
- A responsive path with round trip below the attempt deadline makes progress:
  retrying a request must not revoke an earlier still-owned reply. Completion
  of the whole multistage handshake also needs enough remaining deadline for
  its subsequent flights. Hello serials remain admissible for the live endpoint
  until the attempt's 60-second deadline, including after another hello retry.
  A fixed 119-entry history plus the latest serial retains all 120 possible
  requests at 500-ms spacing (480 bytes of serials per endpoint, 15 KiB across
  32 endpoint slots). No eviction shortens that window. Source, instance,
  ambiguity, stage and transport/attempt retirement checks still apply;
  short replies are rejected because they carry no attempt identity. This is a
  local correlation policy, not a native 500-ms reply TTL.
- After UDP precheck, each endpoint owns independent
  [direct and relay negotiations](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udp-establishment) with the [specified task scheduling](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/session.md#timers).
  The task-creation arguments remain fixed despite later NAT observations.
  Every owned negotiation keeps progressing until success, failure or its
  deadline, whatever another path does. Direct and relay retry clocks are
  independent: progress on one branch must not postpone the other. Relay refusal
  retires only relay work; endpoint-wide offline/asleep alerts retire both.
- The attempt owns the bounded union of direct candidates supplied by servers,
  with per-candidate handshake authority. Endpoint loss does not invalidate
  already-discovered peer links. LinkFailed retires that exact candidate and
  clears its handshake authority; an attempt-local tombstone prevents queued
  replies or later punches from resurrecting it. Repeated punches cannot erase
  other live candidates. Tombstones consume the same fixed candidate budget.
- Each ready relay endpoint owns its allocation ID and ACK effect. The selector
  may prefer a pending relay but must reconsider every ready, connected endpoint
  when that preference fails. Data uses the selected endpoint's allocation ID.
- Establishment retires losing operations, including Opens on retained UDP
  recovery endpoints. It retains the selected relay ACK and authenticated direct
  punch recovery. Retired tokens have no authority; Ready is emitted once.

Before the 60-second deadline an attempt may fail for exhaustion only when no
owned discovery, direct negotiation, live candidate or relay alternative remains.
An explicit attempt-wide edge failure or cancellation can end all paths. After
Ready, selected-link failure ends the session; losing-link failure cannot do so.
These local ownership/tombstone policies are conservative implementation rules,
not extra wire fields. All tables remain bounded by the existing budgets.

Relay diagnostics are local observations returned by the pure core. A fixed
`RelayProgress` snapshot retains the furthest relay milestone, saturating 16-bit
request/result counts, distinct refusing-path counts, and the number of live
paths awaiting information. Requests count emitted Send actions, not network
delivery; duplicate authenticated results count without advancing wire state.
Phase-change actions occur once per milestone. Ready and Failed carry terminal
snapshots, preserved after cleanup. The proxy consumes diagnostic actions at the
owner and logs only typed counts, booleans and fixed labels at info level; failure
summaries add a trusted stalled-stage description and the original refusal bits.
No endpoint, identifier or payload appears in these fields. Relay progress is
observable without changing timers, selection, or the 0241 flag.

Transport telemetry is opt-in for the internal composition (`Config.StatsInterval`),
and defaults to one second in the command. Zero disables DEBUG telemetry collection and sampling. Discard
accounting and summaries remain enabled; see [transport telemetry](docs/reference.md#transport-telemetry).
With collection off, optional queue scans and timestamp allocation are skipped.
Existing pressure/oddity debug logging uses a fixed per-attempt 64-record scalar
budget with no allocated collector; default info logging does not update that budget.
RDT counters belong to its single owner and expose immutable `Counters` and
`Stats` views; admission timestamps occupy preallocated parallel arrays only
when enabled, bounded by configured packet ceilings. They do not enlarge the
protocol packet entries when disabled. Snapshot age queries clamp regressing
time. The current missing-head age resets when the admission watermark moves;
oldest gap age instead uses the oldest retained out-of-order packet, whose
receipt is still proof of a missing earlier sequence.

The edge owns fixed 256-channel counters, 512 layer-inbox timestamps, a
16-handoff secure-send byte ring and a paired 16-entry timestamp channel. The
RDT SendCapacity bound includes the in-flight write, so timestamp publication
cannot wait for I/O; the sole secure writer receives each timestamp after its
matching datagram. Their channel is installed before secure workers start and
never replaced. Completion callbacks update wait counters only at the owner.
The existing timer worker schedules telemetry alongside layer deadlines; a
stats-only wakeup samples snapshots without injecting protocol events. No new
goroutine or protocol deadline is introduced by telemetry. Debug event storage
is absent: at most 64 records are emitted per interval and excess observations
increment a fixed drop count. Periodic output is bounded by the 256 channel
slots; the command's nonblocking logger retains the existing shutdown contract.

Tunnel counters are optional owner-only storage: a fixed 256-slot array and a
256-bit changed-slot set. Native admission, consumption, discard, control
retention, complete handoff and release hooks preserve same-Step transitions.
The edge consumes final affected-slot snapshots rather than scanning all channels
before and after every event. A terminal write/release clears delivery bytes and
age; channel release ends paused time even when PAUSE was queued earlier in that
same Step. Per-slot totals survive reuse, while live clocks follow current state.
Periodic snapshots may scan the fixed slots, independently of the hot path.

`telemetryFlow(cmd, slot, q)` is called after successful control retention with
exact policy Q; `telemetrySent` counts only complete RDT acceptance. LOGIN's body
prevents opcode 07 from becoming a WAKE observation. `VisitTelemetryChanges` drains
numeric final-state metadata without exposing queue storage or payload. The
fair-pause integration records desired PAUSE/RESUME transitions and newly retained
WAKE opportunities from its fixed channel state. Repeated pending WAKEs coalesce
without inflating retention counts. Full acceptance of the immutable `flowTX`
frame counts emission, including an old generation's frame completing after
release; generation checks still protect the new channel's accepted pause state.
Zero, partial, stale and failed acceptance never count emission. Snapshot state
reads desired and accepted PAUSE, coalesced pending controls, WAKE pending state
and the selected handoff's command/accepted prefix directly from this storage.
Current pressure and decision pressure both use the policy's attributed Q:
`C + floor(R*C/T)` for nonempty holders, zero for empty channels. Configured high
and low water and current local/accepted pause state are explicit log fields.
Retention totals describe decisions that can later cancel; emission totals are
complete local RDT handoffs, not proof of physical peer receipt.

RDT keeps feedback cause metadata aligned with the bounded transmit timestamp
array, so protocol packet entries do not grow. Retry causes form a partition:
force first (SACK or exact DATA ACK), then RTO, then urgent 20ms pacing. Received
feedback counts accepted valid frames/ranges, including redundant feedback.
Window/queue gauges describe local DATA state; the remote sender window remains
unknown. Queue byte peaks and maximum missing-head age preserve short excursions
between snapshots. No new wire field or flow/timer policy is introduced.

Pressure and tolerated RDT/tunnel oddity debug records share the same 64-record
interval budget. Stats deadlines saturate instead of wrapping; at epoch
exhaustion the final batch retires its timer, preventing per-event sampling.

## Internal stream interface

The internal application stream interface lives in `github.com/zmerlynn/seestar-wormhole/internal/proxy`.
All application packages are internal implementation details; there is no Go API.
The command and simulator use the existing seams within the module:

```go
s, err := proxy.New(ctx, proxy.Config{Idle: 0})
// Always defer s.Close() after successful construction.
c, err := s.DialPort(ctx, uint16(445)) // (net.Conn, error)
// Every returned connection also implements interface { CloseWrite() error }.
```

`New` loads environment credentials and synchronously resolves the device from
the account catalog with a 30-second request deadline, even when `Config.Serial`
is supplied. That serial selects an existing catalog entry; `Config.Model`
disambiguates several models for one serial. A unique serial keeps its catalog
model. Startup network failures retry with the session backoff until
selection succeeds or the caller cancels; malformed/configuration outcomes fail.
The resolved identity remains fixed for the lifetime of the owner. Zero Idle acquires eagerly; positive Idle acquires on first
client and releases after that interval with no clients. `DialPort` cancellation
bounds admission, while net.Conn deadlines apply after admission. `CloseWrite`
sends tunnel END and preserves reading; `Close` releases the individual channel.
Use `Session.Close()` to cancel all streams, join workers and attempt cloud stop.
In-process consumers can depend on `DialPort(context.Context, uint16) (net.Conn,error)`.
One Session must be shared by all in-process users of a device. Attaching to a
proxy uses its configured local TCP listener and does not allocate another session.

The runtime extends `sessionedge.Factory` with an optional bounded `Driver.Work`
queue. Only the existing session owner executes these completions and all IOTC,
RDT and tunnel Step calls. Cloud, DNS, socket and Pion workers shuttle bounded
results; no worker calls a pure machine. The internal stream has one outstanding
read reservation and one outstanding delivery, at most 8192 bytes each. A slow
reader delays only its own LocalWritten completion.

## Composed runtime and daemon

`proxy.New` wires concrete `sessionedge` adapters. The session owner drives
IOTC, RDT and tunnel through a bounded FIFO of completions, with iterative layer
feedback and one replaceable timer worker per attempt. `internal/edge/iotcedge` owns the
shared UDP socket, up to 32 TCP alternatives, framing, DNS and attempt-scoped
crypto. Pion is confined to `edge.SecureDTLS`; authenticated version and suite are
logged. An attempt cancels and joins all edge workers before cloud stop and any
replacement attempt. The cloud adapter maps busy explicitly, treats failed
internal cloud stop as bounded `ErrReleaseAbandoned` (preserving refusal),
retries network interruptions with fresh detail/online-link, and treats malformed
or unknown allocation outcomes as `ErrUncertain`, blocking reacquisition.

The internal interface caps pending/active/retiring registrations at 128, with a 128-entry
byte-completion inbox and a 257-entry command queue. Each registration reserves
two command slots (Client and ClientGone). Retirement returns registration capacity only after the owner
consumes ClientGone. Already-canceled calls never register; atomic nonblocking
Client submission means an unsent admission never requires cleanup. A stream reserves a tunnel read only when a
writer requests one. Tunnel receive storage is capped at 16 MiB per channel and
64 MiB aggregate; tunnel output at 1 MiB. RDT retains at most 256 transmit and 8192
receive packets, with 1 MiB/8 MiB byte caps. The receive envelope holds the [native
6500-packet window](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#window-and-pacing) and append allowance; the next missing contiguous packet reserves a slot and
1280 bytes, even behind a short readable prefix. Saturation is unacknowledged loss, never a capacity abort. Duplicate
read demands are coalesced before entering the fixed composition inbox. The secure output queue has 16 packet
slots, charged until the IOTC socket write completes; DTLS ingress/egress queues
each have 32 slots and its handshake
admission cap remains 256 KiB of control-record bytes and 512 admitted
control-bearing datagrams over the packet adapter's lifetime. A datagram with
multiple control records increments the count once; application-only datagrams
do not consume that count. The session reserves 80 MiB for
tunnel, 16 MiB for RDT, 8 MiB for each other layer, 2 MiB inbox storage and 256 KiB
per admitted channel. At 128 clients this sums to 154 MiB beneath the 160 MiB
policy ceiling; the count ceiling is 40960. These conservative
reservations bound application buffers; the tunnel reserves 12,288 live objects
for up to 8192 receive chunks plus TX, control and edge work, and factory
admission requires that enlarged byte/object envelope. kernel socket buffers and Pion/library
object overhead are additional bounded edge costs, not a claim of exact RSS.

`proxy.Serve` owns supplied listeners and caps clients at 128. Each has two copy
workers and two 32 KiB copy buffers, independent of siblings. Cancellation closes
both sockets before joining. The production shutdown path cancels I/O immediately
and bounds cloud stop to five seconds; `sessionedge.Run` retains its defensive
125-second maximum (120-second overall dial plus five-second stop) for a
noncompliant adapter. The command's nonblocking stderr descriptor drops log lines
when a pipe is full, so logging cannot hold up ownership release. No secret values,
raw transport errors or protocol action structures are logged.

See [site protection](docs/reference.md#site-protection), [port mappings](docs/reference.md#port-mappings), and [container arguments](docs/reference.md#containers).

See [device selection](docs/reference.md#device-selection) and [installation identity](docs/reference.md#installation-identity).

See [port mapping syntax and defaults](docs/reference.md#port-mappings).

See [metadata permission refusal](docs/reference.md#destination-state-and-safe-paths).

Account lifecycle invariants (replacement safety policy):

- **A credential rejection is terminal.** Classify the received login status at
  headers; body timeout, truncation or cancellation cannot authorize another
  credential request. Automatic session retry cannot revive
  rejected credentials; the operator must explicitly supply credentials in a new
  client. A replay's HTTP 401/403 does not identify bad password, lockout, region,
  or another specific cause. Its original completion retains the received status;
  later operations report reauthentication required without inheriting that
  status or rejection cause. Independent fatal failures remain fatal.
- Initialization with a known purely retryable result remains resumable; retire
  its challenged token and sequence before starting a fresh login. Cancellation
  after admission with no published result is provisional uncertainty owned by
  the account worker. Startup and attempt edges join its immutable publication
  before applying retry policy; attempt cancellation does not abandon that join.
  Safe network completion enters bounded jittered retry. The owner classifies
  the late completion before accepting further authorization; completed malformed,
  denied and fatal outcomes stay blocking across login/profile and both renewals.
- [Ordinary profile 401 is a bounded protocol challenge.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#account-client-401-driven-credential-replay) Independent fatal or
  other blocking causes in the same observation prevent credential replay.
  Logout/expiry join retained failures rather than overwriting their disposition.
- **Local policy:** a remote 401 for distinct older token bytes is stale after
  successful replacement. Identical raw bytes still count against the current
  token. [This differs from the app, which clears its current session even for
  an older request's 401](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#remote-client-token-expired-event); server session scope remains unresolved. Cancellation
  cannot erase expiry. The edge retains one last published token solely for
  correlation through tokenless retirement, consumes expiry once, and clears
  that slot on shutdown. Duplicate 401s never trigger extra credential sends
  (the serialized, bounded replay policy); they cannot grow retained outcomes.
- Cleanup reads the current account token when building stop, including a token
  installed by replay while profile I/O is pending. There is no binding between
  allocation and token. Stop carries only conId, attempts once with an independent
  deadline, and never asks for credentials. With no current token it records
  `release_not_attempted` and returns reauthentication required. [Server acceptance
  of stop with a different token remains unresolved.](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#reports-and-stop)

The composed account lifecycle property checks terminal rejection after each
recovered request/body barrier, across both permitted renewals and lifecycle schedules.
Shutdown schedules check that closed ownership cannot issue further requests.

The composed simulator uses `testing/synctest` virtual time. Its line peer
accumulates split requests, handles coalesced CRLF requests and correlates IDs.
Requests use the recovered `params.keys` shape; returned `state` and padding
fields are explicitly synthetic. Both permitted DTLS suites, verification,
seeded client operations, loss/duplication/reordering, the 1388-byte path MTU,
slow consumers, sustained bursts, cancellation, transport failure and redial
are exercised. RSA seams retain the limitations described in the
[developer guide](docs/development/README.md#test-layout-and-evidence).
Workload seeds and operation barriers reproduce logical workloads, not cryptographic bytes or
Go scheduling. A separate composed loopback seam runs real IOTCIO send/receive,
localhost resolution, and TCP decoding with seven-byte stream fragmentation.
It does not contact vendor DNS or cloud infrastructure.

An eight-hour virtual run polls every minute and checks owner budgets and live
Go heap after GC against an 8 MiB growth allowance. This is simulator heap
evidence, not an eight-hour wall-time device RSS/FD measurement. The slow reader
test transfers 96 KiB while 32 siblings sustain large requests; 128-client bursts
and 300 sequential clients plus four concurrent admissions are separate gates.
These simulator checks do not establish device interoperability; recorded
device coverage is listed separately in [status](docs/status.md).

### End-to-end composition invariants

- **Backpressure never kills the session.** RDT SendCapacity limits outstanding
  handoffs; SendComplete returns capacity only after DTLS Write and the IOTC
  socket receipt. RDT retains unsent payload within its existing byte/packet
  budgets and coalesces deferred unreliable controls by type. DTLS WriteTo and
  TCP workers wait for capacity with cancellation. The owner never waits on
  worker I/O: it pauses an IOTC effect batch and resumes when the worker accepts
  it. Bounded RDT/tunnel processing and local cleanup continue independently
  while IOTC is stalled. At 256 queued input events, excess network datagrams are treated as loss,
  leaving the rest of the 512-event inbox for bounded local ownership/completion
  events. DTLS/RDT recover datagram loss; normal fullness is never session loss.
- **Cancellation of pending work never consumes capacity needed by live sessions.**
  Admission reserves its cleanup command until the owner consumes it. Repeated
  canceled calls cannot reuse a reservation while old cleanup remains queued.
  Control commands have a separate single reservation. Exhaustion rejects new
  admissions with ErrBudget or backpressures control callers; it never cancels
  the session. No cleanup is emitted for work that was never submitted.
- **Every opened channel has exactly one owner until closed.** Registration owns
  a pending dial; verification owns its late result even after cancellation.
  A late Opened without a registered stream immediately generates LocalClose.
  Temporary channel/verification capacity preserves the bounded pending client.
  At most eight clients may await an open for up to 35 seconds each, including
  channel quarantine. Verification expiry retries with a fresh correlation ID
  and the same five-second per-attempt wait; denial or mismatched ports fail.
  Admission deadlines never extend, and cancellation retires verification.
  Local output fullness defers the open without failing the session. Session
  failure and release still end all pending opens. Wire quarantine is unchanged.
- **Shutdown joins every session goroutine.** The attempt closes packet paths, joins
  the maintained DTLS library reader and handshake FSM, joins composition and
  socket workers (whose Write returns only after its context watcher exits),
  then completes cloud release before permitting replacement.
  `internal/edge/PION-PATCH.md` documents the narrow vendored lifecycle integration and
  update procedure. Identity-specific tests inspect the production connection
  immediately after Session.Close; they do not rely on a later scheduler wait.
- **Clean EOF occurs only after every delivered byte is read and the peer ended
  cleanly.** Terminal publication follows delivery. A reader selecting END/CLOSE
  rechecks the delivery slot after observing closure, including partial reads.
  END drains tunnel storage; CLOSE immediately releases tunnel storage, while
  bytes already delivered to the internal stream remain readable.
- **Any rejection or failure surfaces as an error with its cause.** Each stream
  publishes one terminal cause. Peer CLOSE retains payload presence, result and
  local END context. Result 2 is ErrSetupRejected with an inspectable
  PeerCloseError.Result; result 1 before the proxy's END and unknown nonzero
  results are PeerCloseError wrapping ErrTransport. Empty/zero CLOSE is
  ordinary closure.
  Result 1 after the proxy's END is treated as ordinary termination because
  the peer uses that same value for EOF in this state; the wire cannot
  distinguish that path from terminal peer I/O failure. No positive SETUP ACK
  is invented.
  Transport failure produces ErrTransport and local Close produces net.ErrClosed.
  Half-close remains independent in each direction; an already-published failure
  takes precedence over END when both are observed by Read.

The seeded composed property checks ownership, queue limits, allocation release,
exact decoded transaction IDs and response content. It generates SETUP/preflight
rejections, bursts of already-canceled and submission-racing admissions alongside
live traffic, and partial reads racing scripted peer END, ordinary CLOSE and
failure CLOSE, as well as slow consumers, transport failures and redial. Seeded
operations and virtual-time delays do not exhaust arbitrary Go interleavings;
focused race repetitions and deterministic ownership gates cover those boundaries.

See [lifecycle diagnostics](docs/reference.md#logs).

Build scratch images on the current arm64 builder (use the native builder
platform for BUILDPLATFORM on another host):

```sh
podman build --platform linux/arm64 --build-arg BUILDPLATFORM=linux/arm64 --build-arg TARGETARCH=arm64 -t seestar-proxy:arm64 .
podman build --platform linux/amd64 --build-arg BUILDPLATFORM=linux/arm64 --build-arg TARGETARCH=amd64 -t seestar-proxy:amd64 .
```

See [container runtime contract](docs/reference.md#containers). The platform smoke test uses network isolation and synthetic environment strings; no cloud or device contact is needed.

## Not implemented

- Legacy tunnel authentication: normal `_Ex` LOGIN is sent, and nonempty legacy
  auth is rejected.
- Optional service mapping and LAN-direct socket bypass in the outbound profile.
  Codecs support service layouts.
- DTLS session resumption.
