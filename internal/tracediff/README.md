# Offline trace comparison

`Compare(left, right io.Reader, options Options) ([]byte, error)` compares two
version-1 recorder JSON Lines streams. It is an internal offline library; the
test harness is `go test ./internal/tracediff`. No command or device adapter is
added. Tests read the existing synthetic RDT, IOTC and DTLS fixtures and literal
synthetic transactions. They never regenerate fixtures.

The JSON report has `status`, `scope`, and ordered `entries`. Status is `equal`,
`different`, or `inconclusive`. Equality means **retained projection only; erased
bytes unavailable**. No payload content, authentication, or interoperability
claim follows from a matching redacted trace.

Entries identify the machine, canonical per-machine instance number, original
left/right transaction sequences, and observation ordinal. Ordinal -1 denotes
the input; actions start at zero and inherit the input's observation time. A
missing sequence member denotes an unmatched side. Different entries contain
field names, never field values. Other statuses are `unmatched`, `unavailable`,
and `inconclusive`, with fixed reasons where applicable. Errors also omit all
input text. Arbitrary strings in a hand-edited trace cannot enter the report.

Instances pair by machine and first occurrence within that machine. Transaction
alignment preserves causal order, looking ahead at most 32 transactions for an
input kind when kinds disagree; ties prefer the left gap. Repeated kinds stay
in order. This is bounded alignment, not a search for an optimal correspondence
between arbitrary executions. Unmatched observations are retained. Separate
machines may interleave differently. A local counter's numeric value can change,
but its equality relationships must agree across paired inputs and actions.
Counters are scoped to a machine instance; peer and port aliases are trace-wide
bijections. Zero/empty values retain their unavailable/unset meaning. [RDT wire
sequences, versions and transmission counts](https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#header) remain literal, not renumbered.

Clocks are relative to each instance's first recorded transaction. Timers use
the same origin, with zero retained as the absent-timer sentinel. UTC is ignored.
`Tolerance` is an inclusive nonnegative duration, defaulting to exact timing.
Regressing clocks remain visible; subtraction overflow is rejected.

A missing end record, sequence gap, nonzero cumulative drops or nonzero drop
causes makes the whole comparison inconclusive: lost transactions cannot be
attributed to a particular machine or time. Matching observations in that span
are marked inconclusive. Definite differences and unmatched observations remain
reported. Peer/port overflow taints the affected observation. Unsupported
summaries and unknown machines are unavailable; unknown record types make the
whole span inconclusive. `LeftUnavailable` and `RightUnavailable` explicitly
declare known machines that an observation source cannot see, such as internal
decisions absent from packet captures. They never synthesize missing decisions.

Default per-input limits are 16 MiB, 256 KiB per line (including scanner framing),
65,536 records including the end record, 32,768 transactions, and 65,536 total
actions. The combined report is capped at 1 MiB. Each limit is configurable;
zero selects its default, negative values are invalid. See [JSON input policy](../../docs/reference.md#json-file-compatibility). Unknown JSON members are ignored. All
decoded state, alignment work and output storage are bounded by these limits;
the byte budget also bounds temporary decode storage for a transaction before
its action count is checked. Record/transaction/action overflows return
`*LimitError`, as do byte, line and output limits. Malformed JSON, torn JSON,
missing mandatory fields, duplicate/regressing sequences and records after an
end return `*InvalidError`. Either error returns no partial report. A clean EOF
after whole transactions without an end is incomplete evidence, rather than a
torn JSON record.
