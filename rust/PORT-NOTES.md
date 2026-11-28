# Port notes

Phase 1 is a faithful port of the Go engine: the same on-disk bytes, the same wire bytes, the same error codes, the
same results on the conformance suite. This file records how the Rust code maps to the Go code, every Go behavior the
port keeps on purpose, every one it must change, and what is still open.

State at row 0: the workspace, the Go-compatible JSON reader and writer, the test seam, the conformance driver and the
differential driver exist. **No engine code is ported.** Every engine request to the Rust driver answers `other` and
names the row that brings it.

## Map from Go to Rust

One Rust module per Go file, with the same name, so the two can be read side by side. Each module is filled in the row
named. A module that is still a stub says so in its first line.

| Row | Go | Crate | Module |
| ---: | --- | --- | --- |
| 0 | `cmd/dost-conform/main.go` | `dostoevsky-cli` | `conform`, binary `dost-conform` |
| 0 | `internal/seam/seam.go` (added to Go after this row was written) | `dostoevsky-core` | `seam` |
| 0 | the sentinel errors in `node.go` and its neighbours | `dostoevsky-core` | `errors` |
| 0 | `encoding/json` as the engine uses it | `dostoevsky-format` | `gojson`, `base64` |
| 1 | `wire/wire.go` | `dostoevsky-wire` | `wire` |
| 2 | `checkpoint3.go` | `dostoevsky-format` | `checkpoint3` |
| 2 | `frame3.go` | `dostoevsky-format` | `frame3` |
| 2 | `recordscan.go` | `dostoevsky-format` | `recordscan` |
| 3 | `admission.go` | `dostoevsky-core` | `admission` |
| 3 | `id_index.go` | `dostoevsky-core` | `id_index` |
| 3 | `idfilter.go` | `dostoevsky-format` | `idfilter` |
| 3 | `location_index.go` | `dostoevsky-core` | `location_index` |
| 3 | `segindex.go` | `dostoevsky-format` | `segindex` |
| 4 | `journal.go` | `dostoevsky-core` | `journal` |
| 4 | `logstate.go` | `dostoevsky-core` | `logstate` |
| 4 | `sync_linux.go, sync_other.go` | `dostoevsky-core` | `sync` |
| 5 | `seglayer.go` | `dostoevsky-core` | `seglayer` |
| 5 | `segmented_node.go` | `dostoevsky-core` | `segmented_node` |
| 6 | `batch.go` | `dostoevsky-core` | `batch` |
| 6 | `cap.go` | `dostoevsky-core` | `cap` |
| 6 | `node.go` | `dostoevsky-core` | `node` |
| 6 | `notify.go` | `dostoevsky-core` | `notify` |
| 6 | `publish_batch.go` | `dostoevsky-core` | `publish_batch` |
| 6 | `publish_queue.go` | `dostoevsky-core` | `publish_queue` |
| 6 | `stream.go` | `dostoevsky-core` | `stream` |
| 7 | `retention.go` | `dostoevsky-core` | `retention` |
| 8 | `backup.go` | `dostoevsky-core` | `backup` |
| 8 | `migration.go` | `dostoevsky-core` | `migration` |
| 8 | `repair.go` | `dostoevsky-core` | `repair` |
| 8 | `upgrade.go` | `dostoevsky-core` | `upgrade` |
| 9 | `workflow_log.go` | `dostoevsky-core` | `workflow_log` |
| 10 | `workflow/store.go` | `dostoevsky-workflow` | `store` |
| 11 | `wireserver/conn.go` | `dostoevsky-server` | `conn` |
| 11 | `wireserver/pool.go` | `dostoevsky-server` | `pool` |
| 11 | `wireserver/sequencer.go` | `dostoevsky-server` | `sequencer` |
| 11 | `wireserver/server.go` | `dostoevsky-server` | `server` |
| 12 | `clienthttp/handler.go` | `dostoevsky-server` | `clienthttp` |
| 12 | `peers.go` | `dostoevsky-core` | `peers` |
| 13 | `sketch/hll.go` | `dostoevsky-core` | `sketch::hll` |
| 13 | `sketch/hot_topics.go` | `dostoevsky-core` | `sketch::hot_topics` |
| 13 | `sketch/latency.go` | `dostoevsky-core` | `sketch::latency` |
| 8 | `cmd/dost-repair`, `cmd/dost-storage` | `dostoevsky-cli` | binaries of the same names |
| 11 | `cmd/dostoevsky-server` | `dostoevsky-server` | binary of the same name |
| 12 | `cmd/dostoevsky-gateway` | `dostoevsky-server` | binary of the same name |
| 14 | (none) | `dostoevsky-py`, `dostoevsky-node` | empty until their binding crates can be added |

Not ported in phase 1: `cmd/plots`, `cmd/sketchreport`, `examples/`, `benchmarks/`, the existing clients.

Where a Go file mixes encoding with file access, the encoding goes to `dostoevsky-format` and the file access to
`dostoevsky-core`, and the row that ports it records the split here. The rows 3 and 4 placements above are provisional
until those files are read for the port.

## Notes known before the port

| # | Note | State at row 0 |
| ---: | --- | --- |
| 1 | JSON bytes are those of Go 1.27.0; invalid UTF-8 is written as EF BF BD and both forms are read | Writer and reader built and tested (below). Checked against Go 1.24.7, not 1.27.0: see open question 3 |
| 2 | The Go toolchain is pinned to 1.27.0 for the differential test and the release script | Not met on the machine this was built on (Go 1.24.7) |
| 3 | Directory-sync denial: test the EPERM branch and a real set-user-ID process natively | Row 4 |
| 4 | `benchmarks/rustcore` is a frozen record | It lies outside `rust/`, is not a workspace member and is not touched by the gates |
| 5 | Backup must refuse an existing destination explicitly | Row 8. The trace `backup-and-migrate` asks for it twice |
| 6 | `TestSegLogCrashAtEveryOperation` becomes a Rust unit test, outside the suite | Row 5 |
| 7 | Group commit keeps today's gather policy | Row 6 |

## Notes from row 0

8. **A nil byte slice and an empty one are different on the wire.** The Go driver writes a nil `[]byte` as `null` and an
   empty one as `""`. `position` on a group with no state replies `"state":null`. The port keeps the difference: such
   fields are `Option<Vec<u8>>`, and each ported function must return the same one of the two as Go.
9. **A Go error can wrap several sentinels.** The Go driver reports the first match in a fixed order ("an error that
   wraps another comes first"). `errors::Error` carries every kind it wraps and `Error::code` applies that order.
10. **The driver reads requests as Go's decoder does.** A key matches a field by exact name, else without regard to
    case. A repeated key takes its last value. `null` leaves a field at its zero value. An unknown field, a value of the
    wrong type, or a number written with a fraction or exponent for an integer field ends the driver with status 1 and
    no reply. A request may span lines.
11. **Invalid UTF-8 becomes one replacement character per invalid byte.** Rust's own lossy conversion writes one
    replacement for a whole invalid sequence, which would differ from Go for a truncated multi-byte character. The
    reader and the writer both follow Go. The Go vectors include these cases.
12. **Nesting to Go's limit of 10,000 must not overflow the stack.** Go's stacks grow; a Rust thread's does not. The
    reader, the writer and the destructor of `gojson::Value` keep their own stacks. A first, recursive version
    overflowed in the test for this.
13. **An unpaired surrogate escape is replaced, and what follows it is read again on its own.** `"\ud83d\u0041"` is
    U+FFFD then `A`. The Go vectors caught an error in the first version here.
14. **Without the seam, two runs of the Go engine differ in more than the record times.** `scripts/differential.py`,
    run with the Go engine against itself, shows differences in the legacy journal and the segment logs, and also in
    the `.idx` and `.bloom` sidecars and in a backup's `journal.v1`; some files also differ in length by a byte or
    two from run to run. Rows 3 to 5 must find each varying input; with the seam set they must all vanish. Receipt:
    `receipts/row-0/differential-go-go-without-seam.txt`.
15. **The source release copies `rust/` as it stands** (`scripts/release.sh`; a test requires every top-level directory
    to be listed). So Cargo's build output is directed to `.dev/rust-target`, outside `rust/`.

## Open questions

Each is isolated in one place in the code, so the answer is a small change.

1. **The seam contract** (`dostoevsky-core/src/seam.rs`) was written before the Go package and does not yet agree
   with it. Go writes both the seed and the block counter as 8 bytes little-endian and takes the seed as a 64-bit
   number; this file uses a big-endian counter and a byte-string seed. The rest agrees: the first clock reading is
   `start`, a request takes whole blocks, `advance` moves the clock without counting as a reading. Go's seam must be
   set before anything is opened. The driver request names are not yet fixed on the Go side.
2. **SHA-256.** `dostoevsky-core/src/sha256.rs` is 80 lines of our own, checked against the standard vectors and 200
   digests from another implementation, used by the test seam only. It is to be replaced by the `sha2` crate. See
   `DEPS.md`.
3. **Go 1.27.0.** The JSON vectors in `testdata/` were written by Go 1.24.7, which writes invalid UTF-8 as `\ufffd`.
   The writer's default is the 1.27.0 form (raw bytes), tested by hand; its older form is what the vectors check.
   Regenerate both vector files with 1.27.0 (`testdata/README.md`) and rerun the tests before row 2 relies on them.
4. **Placement.** `peers` and the `sketch` package are in `dostoevsky-core`; `clienthttp` and the gateway binary are
   in `dostoevsky-server`.
5. **Row 0 mirrors the Go driver as it was before the workflow requests.** Those, and the named kill and failure
   points, join the Rust driver in a later row.
6. **The system random source.** Unset, the seam reads `/dev/urandom`. Platforms without it need another source before
   the workflow layer (row 10) can run there.
7. **Floating-point numbers and times in Go's JSON format are not written yet.** Each is added, with vectors, in the
   row that first needs it.

## What row 0 does not show

- Nothing about the engine. The conformance run with the Rust driver fails every case that opens a store, as it must.
- Nothing about wire bytes. The differential driver compares stores and driver replies; comparing wire bytes needs the
  server (row 11).
- Nothing on ARM64, macOS or Windows. Everything ran on one x86-64 Linux machine.
