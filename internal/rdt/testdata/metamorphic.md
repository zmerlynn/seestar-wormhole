Synthetic receive transcripts for RDT framing and selective acknowledgements.
The complete 15-message catalog is retained as literal bytes and complete
Pion-authenticated DTLS records. See [synthetic dtls fixture model](../../wiretest/README.md).

Gzip JSON owner transcripts retain every setup and target event and every action
for the baseline and fixed mutation. Setup uses local ID 7 and learned peer ID 9.
ACK tests have an outstanding transmitted DATA; FIN_ACK has a transmitted FIN.
All sequence/range fields are independent literal zeros, with the required all-ones
FIN sentinel. A Read and delayed Tick expose retained data and delayed ACK effects.
The fixed mutation also inserts a bounded unknown packet with a foreign ID and
nonempty payload before known traffic. Variable mutations are re-authenticated
as DTLS before decoding. Unknown packet and SACK capacity work is bounded.

The semantic projection removes the opaque header word; SACK retains only count
and active inclusive ranges. ACKs must copy the input opaque word exactly; fresh
sender packets must contain zero. These properties are checked before comparing
semantic actions. All other machine state, queues, timers and action fields must
match. Independent literal sender goldens remain unchanged. Extra DATA bytes are
application bytes, not permissible tails; no fixed control suffix is invented.
