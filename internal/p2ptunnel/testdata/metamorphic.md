Synthetic receive transcripts for tunnel framing and channel handling.
Every row retains complete baseline and fixed-mutation tunnel bytes, an enclosing
RDT DATA packet and a Pion-authenticated DTLS record. See [synthetic dtls fixture model](../../wiretest/README.md).

The gzip JSON transcript retains every exported event of the internal machine and resulting action
from Start, including login, local Open events and all Sent completions. Agent
channel cases allocate channels 1 through 7 so channel 7 has real authority and
active siblings. Server cases log in and allow port 4700. Port responses correlate
the real outstanding ID 1 and requested port 4700; service replies have no optional
mapping operation in this profile. Cases include legacy/current SETUP, every
established command, LOGIN/empty LOGIN, complete short/full PASS, and REJECT.

Fuzzing changes only consumed-semantic equivalents: session header channel,
SETUP family, padding, service tail, trailing bytes and allowed suffix, CLOSE
suffix, verification spare bytes, request validity and nonzero response validity,
service field tails, and full PASS flag normalization,
ignored signed result/direct/reserved bytes and suffix. Complete short PASS stays
short because length selects capability semantics. Unknown established records
precede known frames and are skipped by bounded length; unknown login is rejected.
Each split-read test compares all actions and retained machine state, excluding
consumed decoder scratch, separately checked diagnostics and repeated scheduling
of the same timer deadline. Sender catalog goldens are independent and unchanged.

No tolerance is inferred for END/PAUSE/RESUME/WAKE or fixed verification-body
suffixes. Receive padding, service tails and validity have explicit tolerance
classifications: the manifest now lists these as `tolerated`, and the
fixed variants and fuzz domain exercise them. Unterminated service IDs, mismatched
port responses and zero validity remain rejected. A service ID is the full string
before its first NUL; a longer string sharing a prefix is a different ID. Optional
service mapping is absent, so unsolicited 0f replies grant no authority.
