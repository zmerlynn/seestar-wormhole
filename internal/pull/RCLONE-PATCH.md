# Embedded SMB transport patch

`vendor/github.com/rclone/rclone/backend/smb/connpool.go` adds
`WithDialContext` and consults its private context key before dialing TCP.
Upstream: rclone v1.75.2, https://github.com/rclone/rclone/tree/v1.75.2/backend/smb.
Its license terms are in `vendor/github.com/rclone/rclone/COPYING` and its SPDX
identifier is listed in `NOTICE`.
This replaces only transport establishment with the existing owned tunnel's
`Session.DialPort(ctx, 445)`. There is no local listener or subprocess.
SMB encoding, authentication, pooling, local copying and verification are upstream.

The hook is context scoped, including pool reconnects; no global mutable dialer
is installed. All returned streams are closed by the SMB pool or on cancellation.
After re-vendoring, restore this patch and the existing Pion lifecycle patch.

`fs/accounting/accounting.go` and `fs/accounting/token_bucket.go` also pass the
transfer context into account rate waits. Upstream uses context.Background,
which makes Ctrl+C/timeout wait for an entire bandwidth reservation (1024 seconds
for a 1024-byte read at one byte/second in the deterministic regression).
The public LimitBandwidth method preserves upstream behavior; Account uses an
internal context-aware helper. Per-file account waits use the same context.
The standard x/time/rate limiter still owns all rate calculations.

Finite deadline fix: Account propagates rejected global and per-file
waits from Read, ReadAt, WriteTo and AccountRead. WriteTo reserves before writing.
The private wait helper uses x/time/rate ReserveN/DelayFrom/CancelAt; a reservation
past the work deadline returns context.DeadlineExceeded rather than continuing
unlimited. Actual cancellation returns ctx.Err and refunds the reservation.
Read accounting may have consumed one bounded input chunk before its wait fails;
the failure prevents subsequent chunks. Independent I/O failures remain joined,
and ordinary EOF retains its Reader semantics. The public LimitBandwidth API
still logs errors under its upstream background-context contract.

Completed-pull close repair: connpool.go joins each connection's
unmount and logoff errors and all pool close errors, retaining failed Echo
probe errors as well. This lets the embedding
command distinguish redundant typed peer closure after verified work from
independent teardown failures. Restore both joins after re-vendoring and run
TestPullCompletedSMBPeerClose (especially mixed-pool). The success-only adapter
is in cmd/seestar-wormhole/pull_connections.go; active work and arbitrary
Shutdown errors retain their failure result.

Progress snapshot race repair (pull-progress): `fs/accounting/stats.go`
keeps the `errors`/`lastError` reads in `RemoteStats` under the existing stats
read lock. Copy the error reference under that lock, then call `Error()` after
unlocking: error implementations can call back into accounting. Progress snapshots run concurrently with worker errors, even though
the CLI only projects numeric aggregates. Without this move the race detector
reports both fields. Restore it after re-vendoring and run
`TestProgressAccountingConcurrentErrors` and
`TestProgressAccountingErrorCallback` with `-race`.

Bounded listing (pull-safety): `backend/smb/listing.go` adds `WithReadDir`
and `ReadDir`. `Fs.List` in `backend/smb/smb.go` opens a directory with
`go-smb2.Share.Open`, dispatches its `File.Readdir(n)` method to the
context-scoped consumer, and closes it on every return (including panic),
preserving read and close errors. Without a consumer, the dispatcher retains
whole-directory reads for other rclone users. Pull always installs the bounded
consumer for inventory; it has no unbounded fallback.

Upstream rclone's `Fs.List` returns only a complete slice and has no incremental
SMB listing hook. Its `Share.ReadDir` calls `Readdir(-1)`, so wrapping the
returned slice cannot bound enumeration. This patch exposes the maintained
library's incremental API without changing SMB framing, parsing, authentication
or query sizes. No go-smb2 patch is needed.

Pull reads 256 entries at a time. One synchronized budget covers all selected
directories: 200,000 entries (including selected roots) and 64 MiB of source
path text (UTF-8 byte lengths, including directory prefixes). Entry count bounds
metadata overhead separately from name storage. It checks before retaining each
page and keeps the existing aggregate inventory counter. A failed enumeration
can produce at most one extra requested page per active reader; go-smb2 may also
buffer entries from one server query response per open directory. These limits
bound enumeration storage, not file content. Raw names are validated before
rclone can clean them with `path.Join`.

After re-vendoring, restore both new dispatch functions and the `Fs.List` open,
dispatch and close path. Run `TestSMBListingBudgetStopsEnumeration`,
`TestSMBListingExactBudgets`, `TestSMBListingReadOutcomes`,
`TestSMBListingRawNames`, `TestRawListingRefusal`, and `TestPullRawListingExit`,
then the pull/command race tests. The synthetic enumerator calls the same
context dispatcher as `Fs.List`; it counts entries produced without Samba or
a network connection.
