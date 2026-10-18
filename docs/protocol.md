# How the proxy speaks the protocol

I use the [v1 specification](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/00-overview.md#protocol-stack)
as the wire reference. This page describes the proxy's part in that stack;
the linked sections carry the layouts and method details. The spec distinguishes
[READ, OBSERVED, INFERRED and unresolved claims](https://github.com/zmerlynn/seestar-spec/blob/v1/CONVENTIONS.md).
Local limits below are choices made here, not device promises. Tested coverage
is in [status](status.md); command mechanics are in the [reference](reference.md).

## Layer by layer

See [architecture and ownership](../DESIGN.md#layering-and-ownership), [port mappings](reference.md#port-mappings), [site protection](reference.md#site-protection), and [discovery](reference.md#lan-discovery).

## Local policy where the spec is silent or unresolved

These include choices permitted by INFERRED guidance and boundaries the spec
leaves unresolved. Permissions already stated in v1 are not open questions.
The [reference](reference.md#protocol-resource-policies) owns the numerical
limits, deadlines and admission mechanics.

| Area and spec boundary | Local choice and reason |
|---|---|
| [Cloud HTTP bounds](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#host-transport-and-headers) | [Local choice](reference.md#protocol-resource-policies). |
| [Cloud JSON](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#response-envelope) | V1 describes the plain Gson converter but specifies no duplicate-member rule for HTTP responses. I reject duplicate names anywhere as local fail-closed policy. Operators see the existing invalid-response validation failure (account failures retain HTTP status); ambiguous data is never installed. |
| [License JSON](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/sdk-licence.md#licence-json) | V1 specifies first-position/last-value duplicates in standalone overrides. I reject all duplicate names as local fail-closed policy. License installation fails through the existing invalid-license path, before session establishment. |
| [Discovery requests](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#device-side-responder) | Duplicate names and invalid UTF-8 cause a silent drop. Decoded names match the specified case exactly; case variants remain unknown members. |
| [Catalog selection](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#device-selection) | [Local choice](reference.md#device-selection). |
| [Login input](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/login.md#input-preparation) | R19 permits my choice to send explicit credentials without app UI trimming. |
| [Credential storage](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#storage-startup-and-source-identifier) | I retain credentials in memory and persist only the installation UUID when requested. |
| [Profile and failed renewal](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/login.md#installation-and-profile-fetch) | R27 requires a usability decision: I keep tokens after pure profile I/O failure and block authorization after malformed or denied outcomes. |
| [Old-token expiry](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#remote-client-token-expired-event) | I retire matching current token bytes on remote 401; server-wide expiry scope remains unresolved. |
| [Stop and uncertain ownership](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#reports-and-stop) | [Local choice](../DESIGN.md#session-ownership-implementation). |
| [Busy and recovery](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/state-timers.md#timers) | See [Local choice](reference.md#availability-and-busy-devices). Service limits remain unknown, distinct from the observed retry guidance. |
| [Installation identity](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#storage-startup-and-source-identifier) | [Local choice](reference.md#installation-identity). |
| [Hello correlation](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#receive-catalogue) | See [Local choice](../DESIGN.md#iotc-path-ownership-invariant). Trailing bytes are tolerated. I require an echo match; whether servers send short replies remains open. |
| [Alternate-source NAT observations](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#nat-classification) | [Local choice](../internal/iotc/README.md#scope-and-local-policy). |
| [Attempt identity](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#attempt-and-cache-lifetimes) | [Local choice](../internal/iotc/README.md#scope-and-local-policy). |
| [Path selection and bounds](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udptcp-race) | See [Local choice](../internal/iotc/README.md#scope-and-local-policy). Native close winners depend on scheduling. |
| [TCP framing failure](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#tcp-reception) | [Local choice](../internal/iotc/README.md#edge-contract). Replacement-client permission remains unspecified. |
| [Optional paths and migration](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/session.md#path-migration) | [Local choice](../DESIGN.md#composed-runtime-and-daemon). |
| [DTLS profile](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#version-and-cipher-suite) | I offer only DTLS 1.2 ccac or 00a8; the full native offer and unresolved runtime configuration leave a compatibility boundary. |
| [DTLS bounds](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#timers-and-shutdown) | Pion schedules flight retries; I bound handshake time and lifetime control traffic without a specified valid-peer traffic maximum. |
| [RDT identity](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#connection-identity) | See [Local choice](../internal/rdt/README.md#wire-behavior-and-deliberate-local-policy). Arbitrarily late zero-ID traffic after reuse has no wire generation to distinguish it. |
| [RDT receive pressure](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#receiving-and-reading) | See [Local choice](../internal/rdt/README.md#wire-behavior-and-deliberate-local-policy). V1 supplies no receive-credit or byte-cap contract. |
| [RDT send scheduling](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#window-and-pacing) | [Local choice](../internal/rdt/README.md#wire-behavior-and-deliberate-local-policy). |
| [RDT ACK policy](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#ack-generation) | [Local choice](../internal/rdt/README.md#wire-behavior-and-deliberate-local-policy). |
| [RDT RTT sampling](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#rtt-and-rto) | [Local choice](../internal/rdt/README.md#wire-behavior-and-deliberate-local-policy). |
| [RDT close](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#fin-retransmission) | See [Local choice](../internal/rdt/README.md#wire-behavior-and-deliberate-local-policy). Practical native FIN latency remains unresolved. |
| [Tunnel malformed frames](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/session.md#timers-and-limits) | [Local choice](../DESIGN.md#tunnel-contract). |
| [Port re-verification](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#agent-behaviour) | [Local choice](../DESIGN.md#end-to-end-composition-invariants). |
| [Tunnel pressure and scheduling](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#pause-resume-and-wake) | See [Local choice](../DESIGN.md#tunnel-contract). V1 does not guarantee equivalence to native shared charging and failed-write bookkeeping. |
| [Tunnel backlog and reclamation](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#backlog-after-pause) | [Local choice](../DESIGN.md#tunnel-contract). |
| [Discovery synthesis](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#stand-in-responders) | [Local choice](reference.md#lan-discovery). |
| [SMB pull assumptions](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/smb.md#open-questions) | I use guest SMB with bounded inventory and copy work; upper dialect, signing and stable metadata guarantees remain open. |
| [Runtime admission and reservations](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/seestar-profile.md#minimal-outbound-proxy-profile) | [Local choice](../DESIGN.md#composed-runtime-and-daemon). |
| [Tunnel channel reuse](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#channel-allocation-and-reuse) | [Local choice](../DESIGN.md#tunnel-contract). |
| [Opaque alert metadata](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/relay.md#server-alerts) | I require complete alerts and matching SID/nonce; unfamiliar triggering record types are diagnostic and do not prevent a recognized alert from changing endpoint state. The alert protocol-version byte remains opaque. |

See [IOTC scope](../internal/iotc/README.md#scope-and-local-policy) and [unimplemented tunnel paths](../DESIGN.md#not-implemented).

## Deliberate deviations

See [site protection](reference.md#site-protection) and [discovery](reference.md#lan-discovery).

The current parser sorts standalone names before selecting ten.
[V1 specifies document order](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/sdk-licence.md#licence-json);
licenses with more than ten distinct names may select different overrides or
validate entries the SDK would ignore. This is an incompatibility with the
specified selection order and the [requirement to honor overrides](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-legacy.md#master-host-names),
not an unanswered selection rule.

The proxy also uses one tunnel rather than reproducing the app's two-session
mapping profile, adds SMB, and omits LAN-direct bypass. Account renewal refuses
stale-token replay after failed credentials. The transport scheduling choices
are called out in the policy table above.

JSON decoding uses case-sensitive field names from the linked cloud, license,
and discovery schemas. Unknown members remain allowed. Invalid UTF-8 and
invalid Unicode escapes are rejected as local fail-closed policy. For site
inspection they receive the same `-32700` answer as malformed JSON; opaque
device replies are unaffected. See [site protection](reference.md#site-protection)
for forwarding and local-answer rules.
