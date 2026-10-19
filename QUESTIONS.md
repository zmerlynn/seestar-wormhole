# Open questions and validation limits

These questions distinguish unresolved peer behavior from this client's local
policies. Current architecture and ownership contracts are in
[DESIGN.md](DESIGN.md).

## IOTC hello compatibility

Do other native client builds apply a tighter hello reply correlation lifetime?
See [hello retention policy](DESIGN.md#iotc-path-ownership-invariant).

Which older servers require the 16-byte hello reply? This profile requires the
24-byte reply with echoed serial and instance; the codec still represents the
short form. Random serial origins reduce predictable reuse but cannot guarantee
uniqueness or authenticate hello replies.

## Refusal behavior

Do requests prolong the server's refusal after a session ends? The client's
bounded retry window is local policy; it does not establish a vendor cooldown
code or duration. See
[session ownership](DESIGN.md#session-ownership-implementation) for the current
retry and cleanup rules.

## Relay and direct traversal

What real backend conditions produce relay result 030e, and is the selected
relay-info flag sufficient for interoperability? See [relay selection profile](internal/iotc/README.md#scope-and-local-policy).

## SMB device evidence

See [SMB protocol and access](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/smb.md#protocol-and-access) and [tested coverage](docs/status.md).

## Cloud metadata

What additional response fields does the service emit? What schema and units
apply to sharing timestamps, and what units or renewal meaning do
`accessExpire` and `refreshAfter` have? These unused members are ignored regardless
of type or null; no timing or conversion is inferred. Consumed fields and JSON
bounds remain validated as described in the [cloud contract](internal/cloud/README.md).

## Legacy tunnel login

See [PASS receive rules](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#login_pass--login_reject-08--09-server---agent).

## PAUSE overshoot

What is the largest device-to-client backlog after PAUSE with default rclone
request pipelining and an HTTP file GET? PAUSE controls future socket reads;
already-enqueued DATA can still arrive, and RDT advertises no receive credit.
No hard peer burst maximum is established.

See [pressure accounting and reclamation](DESIGN.md#tunnel-contract).

## Application behavior

How does initialization ordering compare across Android, iOS and remote paths?
The proxy forwards application messages without synthesizing initialization or
maintaining application state.

Full ViewPlan payloads, observed reply/event ordering and firmware matching
remain open. Full RTSP event payloads and exact observed build matching also
remain open. Native handling of discovery `name` and `ip` members also remains
open; this proxy's discovery replies are synthesized.
Forwarding does not require a complete event schema.

## Server-list refresh

See [master reply and server-list retention](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#received-inner-messages).
