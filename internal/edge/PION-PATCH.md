# Pion lifecycle integration

go.mod replaces Pion v3.1.10 with the fork tag
[`github.com/zmerlynn/dtls` `v3.1.10-wormhole.1`](https://github.com/zmerlynn/dtls/tree/v3.1.10-wormhole.1),
which changes only `conn.go` and adds its join test. `go mod vendor` reproduces
the patched file. The changes are:

- Add `workerLoops sync.WaitGroup` to Conn and use it in place of the existing
  handshake-local WaitGroup for the two handshake workers.
- Public Close first calls existing `close(true)`, waits for `handshakeDone`
  (which serializes worker creation), then waits for `workerLoops`.
- Move the reader's Done into a defer registered before its cleanup defer, so
  completion follows decrypted-channel closure and handshaker cancellation.
- In `contextWithClose`, add a `done` channel, defer its closure in the watcher,
  and receive from it after `stopOnce.Do` in the returned cancel function.
  Every cancel caller waits; Write's existing deferred cancel joins its watcher.

Internal workers still call `close(false)`, never public Close: they cannot join
themselves. No cryptographic, packet, handshake or wire behavior changes. The
fork's `wormhole-close-join` branch carries the same commit for upstream
submission. On a Pion upgrade, rebase the commit onto the new tag and retag the
fork, or drop the `replace` once upstream supplies the same completion contracts.

## Goroutine audit

Audited every Go `go` statement in vendored `dtls/v3` and `transport/v5`:

| Site | Owner and observed completion |
| --- | --- |
| `dtls/v3/conn.go`, handshake FSM and reader (2) | Conn `workerLoops`; public Close waits after handshake creation completes. |
| `dtls/v3/conn.go`, `contextWithClose` (1) | Write's deferred cancel waits on watcher `done`, including repeated/concurrent cancellation. |
| `dtls/v3/internal/net/udp/packet_conn.go`, listener read loop and socket closer (2) | Listener `readWG` counts both; listener/last PacketConn Close waits after listener acceptance and all connections retire. These belong to a shared listener, not an individual Conn; `ClientWithOptions` over the proxy-supplied packet path does not start them. |
| `transport/v5/netctx/packetconn.go`, read and write cancellation helpers (2) | Each operation closes its local `done` and waits on its local WaitGroup before returning. PacketConn Close closes underlying I/O before acquiring read/write locks. |
| `transport/v5/netctx/conn.go`, read and write cancellation helpers (2) | Same local WaitGroup contract; this stream wrapper is not used by the DTLS packet path. |

The first-party wrapper `internal/edge/dtls.go` starts one lifetime watcher: `SecureDTLS.Close`
closes stop and waits for joined. It closes packets before Pion Close, unblocking
I/O. Composition closes SecureDTLS and then waits for its own workers, including
the writer; the writer's return includes the per-Write watcher join. Public
Pion Close alone does not join arbitrary application callers concurrently using
Write; the composition owns and joins those callers.

`TestDTLSCloseWaitsForReaderExit` holds the real reader in its logger cleanup
hook. `internal/edge/testdata/pion_context_join_test.go` holds the exact context watcher at
Done evaluation; cancellation and subsequent Close must wait for release. Run
the check in `docker.io/library/golang:1.27` from the repository root:

```sh
podman run --rm --network none -v "$PWD:/src:ro" -w /src \
  -e GOFLAGS='-mod=vendor -p=2' -e GOCACHE=/tmp/go-build \
  -e GOMODCACHE=/tmp/go-mod docker.io/library/golang:1.27 \
  sh scripts/test-pion-join.sh
```

The script copies the vendor package to container temporary storage and adds
this package-private test there, keeping the vendor tree's sole difference
confined to conn.go. Identity-specific composition tests also inspect the
connection immediately after Session.Close.
