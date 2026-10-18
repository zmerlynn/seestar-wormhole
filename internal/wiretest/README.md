This package is test support for immutable synthetic receive transcripts. Pion
encodes and authenticates DTLS 1.2 epoch-one application records using the
[negotiated-profile ChaCha20-Poly1305 cipher](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#record-protection), a public 32-byte `42` key and 12-byte
`24` IV. It models an already authenticated connection, not a handshake or replay
window. Each test invocation is a separate synthetic connection. Live credentials
and vendor traffic are never inputs. Tests re-authenticate mutated payloads and
check the unmodified, checked-in protected records independently.
