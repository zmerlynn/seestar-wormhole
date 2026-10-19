// Package iotc implements bounded revision-29 outbound rendezvous, packet framing,
// legacy obfuscation and direct/relay session ownership as pure single-owner
// Step(event, now) machines. DNS, UDP, TCP, randomness and cryptography are explicit
// edge effects. A Machine and its crypto context are single-use per attempt.
//
// It owns bounded framing, modern control records, rendezvous state and
// attempt-local key caching; networking and cryptographic operations belong to
// the edge adapter. README.md specifies adapter ownership and deferred paths.
// No IOTC path readiness implies DTLS authentication; datatype-1 packets
// must pass through the independent DTLS edge before reaching RDT.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/index.md
package iotc
