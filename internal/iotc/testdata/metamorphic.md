These are public synthetic fixtures for IOTC framing and rendezvous controls.
They contain complete decoded inner bytes,
complete on-wire records, public synthetic keys/IVs, and a fixed permissible
mutation, rather than captured account or device traffic. [delete the quoted passage]
GCM uses crypto/aes and crypto/cipher. ECDH/DER uses crypto/ecdh and crypto/x509.

`metamorphic-transcripts.json.gz` retains every owner event and resulting action
for each baseline and fixed mutation. Modern controls, alert and knock scenarios
include setup from Start using the attempt fixture peer. Legacy peer scenarios
start at the explicitly established/hello contexts below. Gzip and JSON have no
custom decoder. Tests read these immutable transcripts; generation is external
review evidence, never an update mode in the tests.

The contexts are the existing synthetic attempt fixture: UID EXAMPLEUID0000000000,
RID 4433221188776655, local record SID 7, authenticated server SID 0x55, master
nonce 0x4321 and P2P nonce 0x1234. P2P server scalar 1 and client scalar 3 derive
the published key using P-256. The master-only scenario uses client scalar 2 and
master key/IV 01/02 followed by zeros. The other scenarios use master key/IV
02/03 followed by zeros. Legacy scrambling uses the zero license mask. Only
master RSA request completion is a structural seam; received authentication is
real GCM. No handshake transcript is claimed to be a live capture.

Modern precheck/punch/relay and device-context controls use the literal layouts
from modern-fixtures.json and the attempt suite. Device-only 010d/0129/012b are
codec-accepted controls with no client operation. 0403 likewise has no client
operation. The other knocks are delivered after authenticated punch authorizes
the source; finals require the earlier 0401 response authority. Alert 1 uses a
known, authenticated server SID and matching nonce.

Legacy hello contexts retain outstanding serial 77, local instance 7, advertised
server 203.0.113.1:10001 and the synthetic observed sockaddr. Legacy established
contexts retain the above RID, secure datatype 1, channel 0, age 17, and direct
peer 198.51.100.20:41000 or synthetic relay 198.51.100.20:41000/allocation 0x2345, as supplied
by the existing established fixture. Complete plaintext and scrambled wire are
both retained. Legacy obfuscation is not authentication; peer application bytes
remain opaque DTLS payloads at the IOTC boundary.

Tolerances are limited to explicitly opaque fields, sockaddr family/padding,
master unknown-unit extents and suffix, knock consumed prefixes, relay-alive and
hello suffixes, alert minimum-length suffix/diagnostic trigger, and the masked
upper datatype bits. No fixed-size modern control receives an invented extension.
Type 5 has no admitted client machine operation; record2 types are not negotiated.

Receive handling tolerates revision and unassigned flags. The manifest's
`Tolerated` list marks revision, bit 1 (no byte swapping), bit 2 and bits 4–7 as tolerated; fixed variants and the fuzz mutation
now exercise them on every legacy header, including GCM-protected inner headers.
Magic, exact lengths, scrambling bit 0, required extended RID bit 3 and all
correlation checks remain required. The original baseline wire bytes are retained.
