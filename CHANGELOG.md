# Dostoevsky changelog

Dostoevsky is an MIT-licensed, pure-Go library for durable messaging and stream processing. Applications can start with a local directory and distribute partitions across application instances when needed. The runtime uses the Go standard library and builds with `CGO_ENABLED=0`.

**Repository status.** The complete 0.2.3 source was merged into this repository on **22 November 2026 at 00:28:38 UTC** (PR #1, before the move to `rodriveracom/dostoevsky`). The versions below follow the six source packages delivered during development.

## Unreleased — named failure and kill points

- Added internal, test-only points in the write paths where a test can inject a failure or kill the process. Applications cannot reach them; unarmed they do nothing.

## Unreleased — test seam for stored inputs

- The clock and randomness that reach stored bytes now go through an internal package so that tests can fix them. Applications cannot reach it, and unset it is the standard library.
- Legacy retention, the legacy export of a segmented store and migration now write partitions and consumer groups in a fixed order (they followed Go's random map order). Contents are unchanged.

## Unreleased — workflow conformance cases

- Added workflow operations to `cmd/dost-conform` and cases for registration, starts, leases, outcomes, signals and a killed process. No change to the engine.

## Unreleased — workflow examples

- Added six examples under [examples/workflows](examples/workflows/README.md): one-shot reservation, task queue, webhook intake, saga, payment callback and text ingestion, each with a restart test. The format targets now include them.

## Unreleased — conformance suite, first set

- Added `cmd/dost-conform`, a driver that runs the engine from outside, case files, and `make conformance`. No change to the engine. The cases cover a first part of past recovery fixes; `cmd/dost-conform/README.md` lists what is not covered yet.

## Unreleased — durable workflow signal waits

- Added a named signal step and a durable, step-addressed Signal call. Waiting runs hold no leases or worker slots. A signal supplies the input to the next activity.
- Added replay, stale-signal, repeated-name and process-kill tests. Older readers reject the new transition format when a signal definition first appears.
- Added a Linux scale benchmark for 100,000 waiting runs and a 1,024-signal burst with one and 32 polling workers. Extended the existing three-activity workload to 30 activities and measured fresh-process recovery. See the [scale report](docs/workflow-wait-scale.md).

## Unreleased — workflow crash recovery

- Added a durable workflow commit boundary and per-store identity. Recovery verifies committed segments and repairs unacknowledged suffixes, including an interrupted rollover. It preserves committed corruption for inspection.
- Added strict adoption of existing segmented workflow stores, fencing of uncertain writes and reads, and bounded grouping of waiting workflow commands.
- Added process-kill, corruption, migration and payload-budget tests plus paired ext4 performance and reopen measurements. See the [recovery report](docs/workflow-commit-boundary.md).

## Unreleased — experimental embedded workflow lifecycle

- Added a separate segmented workflow store with immutable sequential activity definitions, exact-input StartOnce, leased Poll, successful or failed Finish, bounded attempts, fixed backoff and paginated History.
- Added bounded group validation, post-sync replies, replay and fail-closed handling of uncertain writes. Preserved independent stream-only builds.
- Added shared SQLite conformance cases, abrupt-exit and damaged-tail tests, a runnable Go example, development Make targets and per-completion measurements. See the [workflow guide](docs/workflow-lifecycle.md).

## Unreleased — fresh durable publishing

- Reduced allocation during segmented publication through bounded frame-buffer reuse, frame size reservation and fewer intermediate batch allocations.
- Added paired fresh-publish benchmarks, single-producer storage timing, a Linux sync comparison, and process-kill recovery tests for one and 32 segmented publishers. See the [benchmark report](benchmarks/fresh/README.md).

## Unreleased — review fixes for the workflow candidate

- An interrupted first initialization of a segmented or workflow directory can be started again.
- A workflow directory in the older format is converted only after its history replays.

## Unreleased — cache admission and hot-topic analytics

- Added optional frequency-based admission for verified sealed indexes, charged to the ID-table budget. FIFO remains the default. A duplicate receipt can reuse the index already decoded during lookup.
- Added a bounded time-decayed hot-topic tracker for Go consumers. Updates retain fixed-size names and probation fingerprints. Top-k queries use O(k) scratch space.
- Added reproducible comparisons with the original FIFO commit, three-way raw benchmark results and analytics accuracy and allocation measurements. See the [benchmark report](benchmarks/admission/README.md).

## Unreleased — review fixes for the council candidate

- `Open` syncs the parent of every directory it creates.
- `dostoevsky-server` refuses a unix socket path below a directory another user can rewrite.
- A segmented store refuses records it could not index (a segment checkpoint near 4 GiB) instead of acknowledging them.

## Unreleased — publishing with retained producer IDs

- Sealed segments now have optional per-segment Bloom filters for producer ID lookups. Full-record comparison still determines duplicate receipts and conflicts. Missing, damaged or mismatched hints fall back to the verified exact index. Retention removes expired hints.
- Added a 64 MiB budget for resident filter bits and an 8 MiB target for cached exact ID tables, with room for one oversized table. Retry lookups read directly from the matching segment.
- Added `PrepareIDFilters` and offline `dost-storage -mode prepare-ids`, deep-history publish and rollover measurements, and native-protocol peer benchmarks with identified messages and restart verification.

## Unreleased — segmented storage recovery

- Added opt-in segmented storage to the public Go node and native server, with format discovery on reopen and durable checkpoints for topology, offsets, floors and consumer groups.
- Added range validation for missing segment files, active-tail recovery, bounded index caching, index reconstruction and a sequential batch reader over sealed segments. Retention reclaims fully expired contiguous prefix segments.
- Added staged migration from existing journals, backward-compatible backup export, a full integrity scan and the offline `dost-storage` tool.
- Added recovery, missing-file, migration and rollback tests, public API scale and catalog benchmarks, an eight-producer process-kill check and development Make targets. See the [storage guide](docs/segmented-recovery.md).

## Unreleased — 27 November 2026 council recovery candidate

- Integrated the strict journal scanner from PR 81. Recovery verifies framing and checksums, extracts the fields needed to index canonical records, and falls back to the existing JSON decoder for other journal entries.
- Compressed physical locations for longer partitions. Each block anchors every 32 entries, bounding the number of entries decoded on a random read. Small partitions retain direct locations.
- Reduced memory for producer IDs by indexing process-keyed fingerprints. A hit reads the full producer ID from the existing journal. Collisions are chained and compared exactly before a publish returns an old receipt or a conflict.
- Preserved the journal format, acknowledgment boundary and public Go API. Added tests for packed-location boundaries and deliberate fingerprint collisions.
- Added a Linux history benchmark for separate seed, reopen and full payload readback processes, with optional decimal or 32-character producer IDs. Added native socket comparison, fresh-ready time, boot memory, and verified batched JavaScript trials. Kafka uses a record batch on one partition. Socket clients pipeline individually acknowledged records.

### Native transport staging

- Integrated the native server from PR 72 and native client source from PRs 62, 75 and 77 alongside the JavaScript/TypeScript client. Added native benchmark adapters for Python, Java, Rust and JavaScript.
- Limited concurrent read and position tasks across connections. Added CLI options for the read-task budget and optional journal byte ceiling. Added coverage for capacity recovery, writes while reads are saturated, and publication at the journal ceiling.
- Added a process-kill test with concurrent native publishers, byte-for-byte readback, and idempotent retry of every confirmed record. Added a matching peer crash harness that restarts JetStream, Redis and Kafka on their original storage and verifies every observed acknowledgment. Added the upstream hash for the pinned CPython 3.12 AMD64 Kafka benchmark wheel.

## 0.1.0 — Embedded topics, durable storage, and stream processing

- **Added an embedded Go module.** Reviewed the supplied Faust source and its word-count example, then implemented keyed streams and recoverable processor state in Go. A single-process application opens a data directory. Kafka, Flink, RocksDB, and a separately deployed broker are unnecessary.
- **Added topics, partitions, and replay.** Producers publish keyed records to named topics. FNV-1a maps a key to a partition. Each partition assigns contiguous, zero-based offsets. Consumers can read an exact offset, replay from the beginning, or wait for new data with context cancellation.
- **Made producer retries idempotent.** An optional message ID identifies a publication within its partition. Retrying the same key, ID, and payload returns the original offset and timestamp. Reusing the ID with conflicting content returns `ErrConflict`.
- **Built the durable journal.** Each node owns a checksummed, append-only file and acknowledges writes after file sync. An exclusive lock prevents two local writers from opening the same directory. Startup reconstructs indexes, truncates an incomplete final frame, and reports checksum corruption.
- **Added independent consumer groups.** Each group tracks its next offset and processor state per partition. `Commit` compares the expected cursor and writes the new state and cursor together. Competing commits against the same cursor produce one winner.
- **Added `Fold` and `Pipe`.** `Fold` runs a typed Go reducer and restores its state after restart. `Pipe` transforms one input into one output, publishes it, and checkpoints the input. A deterministic output ID makes retries safe across the failure window between publication and checkpointing.
- **Added distributed routing.** `Mesh` routes publish, read, position, and commit operations to a partition’s owner over an HTTP handler embedded in the application. Peer requests carry a bearer token, intended owner, and topology fingerprint. The configuration permits adding topics on reopen and protects existing partition assignments.
- **Shipped two runnable examples.** `examples/wordcount` preserves counts across process restarts. `examples/mesh` runs two application processes that publish to each other’s partitions. Both were exercised locally, including delivery in both directions.

## 0.1.1 — Development, release, and deployment commands

- **Added a top-level Makefile.** `make help` lists the commands. `make dev` checks formatting, runs `go vet`, tests with cgo disabled, runs the race detector, and builds the examples. Demo binaries and journals live under `.dev/`.
- **Added source release packaging.** `make release` produces a versioned tarball and SHA-256 checksum. `make release-check` verifies the checksum and reruns offline tests from an extracted copy.
- **Added deployment for applications embedding Dostoevsky.** `make deploy-build` builds a selected Go `main` package for Linux AMD64 or ARM64. `make deploy` installs the executable atomically into an explicit destination. The host application controls its journal, credentials, and restart.
- **Verified the deployment workflow with a separate Go application.** Built and ran the installed AMD64 executable, repeated the installation, and cross-compiled a statically linked ARM64 executable.

## 0.2.0 — Keyed notifications, batch reads, and storage interfaces

- **Added `Notifications[K]`.** A reader subscribes to a key before checking durable storage. After a successful commit, the writer signals that key and wakes the current subscribers. Channels are allocated lazily, active subscriptions are capped, and releasing the last subscriber removes the key. This supports database-backed readers without constant polling.
- **Added `Node.ReadBatch`.** Readers request a page with both record-count and byte limits and receive its resume cursor. A waiting read returns as soon as the first record is available and collects whatever else is ready. An oversized first record returns `ErrLimit`, allowing a retry at the same cursor.
- **Split storage capabilities into small Go interfaces.** Added `Publisher`, `Reader`, `Checkpointer`, `BatchReader`, and `BatchCheckpointer`, while preserving the combined `Store` interface. An application can supply its own batch reader and retain SQLite as its durable store.
- **Added `ConsumePages` and `Node.CommitBatch`.** A page consumer receives the expected prior cursor and the page’s next cursor. A SQLite adapter can commit projection output and cursor advancement in one transaction. Journal-backed consumers can advance several offsets and replace processor state in one conditional commit.
- **Added recovery and concurrency tests.** Covered subscription caps, shared wakeups, idle-key cleanup, byte and count boundaries, stale checkpoints, cancellation, and recovery of a multi-record checkpoint after reopening.
- **Added `make dev-bench-notify`.** On the tested Linux AMD64 host, repeatedly signaling a key with eight already-notified subscribers measured **13.00 ns/op and zero allocations**. This measures the signaling operation alone. End-to-end delivery latency and durable publish throughput require separate measurements.

## 0.2.1 — Public Go terminology and onboarding

- Replaced internal organization-specific terminology throughout the source, tests, benchmarks, and README. The documentation distinguishes a *consumer* that reads records, a *consumer group* that owns a checkpoint, and a *subscriber* that waits for a notification key.
- Reorganized the README around a runnable word-count example, followed by topics, keys, partitions, offsets, replay, and checkpoints.
- Added a tested Go documentation example showing how to subscribe to partition changes before checking storage.
- Rewrote the batch benchmark guidance around a portable workload: one consumer drains a backlog while another waits at the end of its partition. This was a source-compatible patch.

## 0.2.2 — OpenAI streaming and offline replay examples

- **Added a model-stream example.** `make dev-openai-text` uses Go’s `net/http` to call `POST /v1/responses` and append streamed JSON events to a Dostoevsky topic.
- **Added a hosted-agent example.** `make dev-openai-agent` calls `POST /v1/agents/sessions` directly, supplies the Agents API beta header, and records a hosted sandbox task through turn completion. Both examples use the standard library without an OpenAI SDK.
- **Added offline replay.** `make dev-openai-replay` reads recorded events from offset zero without credentials or another billable API request. An interrupted stream leaves a replayable prefix and reports incomplete termination.
- **Verified the HTTP behavior with local fixtures.** Tests cover both request paths, streamed event parsing, and a disconnect before completion. **Live OpenAI execution remains unverified.**

## 0.2.3 — Durable background task queue example (22 November 2026)

- **Added `examples/taskqueue`** with `make dev-task-enqueue`, `make dev-task-drain`, and `make dev-task-inspect`. A producer can submit a named job and exit. A later worker process drains the durable queue.
- **Demonstrated partitioned workers and acknowledgments.** Four partitions share one consumer group, with one worker goroutine per partition. The worker publishes a result, retry, or dead letter before committing the input offset.
- **Demonstrated three job outcomes end to end.** A SHA-256 task succeeds on its first attempt. A simulated transient failure succeeds on attempt two. A persistent failure produces a dead letter on attempt three with the original input and failure reason. Unknown tasks go directly to dead letters.
- **Tested the critical restart window.** After outcome publication and before acknowledgment, an interruption causes the handler to run again on restart. Stable IDs deduplicate the example’s retry and result publications. External effects such as sending email still require idempotency at the destination.
- **Module path is now `github.com/rodriveracom/dostoevsky`.** Dostoevsky is a standalone community project. Update your import paths and `go.mod`; GitHub redirects the old repository URL.
- README: added install and contribute sections.

## Verification and operating limits at 0.2.3

The final source package passed `make release` and `make release-check`, including formatting, static analysis, unit tests, race detection, checksum verification, and offline tests from the extracted release. The full ZIP’s SHA-256 is `589e885ab11a813ceb0b8120efef7d33796a15c11c8fc31367226d322ce131ea`.

| Area | Current boundary |
|---|---|
| Availability | Each partition has one statically assigned owner and one durable copy. An owner outage makes its partitions unavailable. Disk loss requires restoring a backup. |
| Storage growth | The journal retains history indefinitely. Startup scans it to rebuild indexes, and writes are serialized per node. |
| Consumer coordination | Applications assign workers to partitions. The task queue example implements immediate retries. Dynamic balancing, worker leases, and scheduled delivery remain outside its scope. |
| Batch transport | `Node.ReadBatch` and database-backed `BatchReader` implementations support pages. `Mesh` uses the single-record peer protocol. |
| Performance evidence | The proposed **100 durable publishes/s for one hour** test, with two checkpointing consumer groups, remains an acceptance target. This changelog records no completed run of that benchmark. |

## 0.3.0 — Burst publishing, dynamic topics, retention and telemetry (23 November 2026)

- Documented durable claims, recipient batching, retry scheduling and separate body storage in `docs/delivery-workload.md`.
- Added a mergeable, fixed-memory latency histogram. The burst harness now reports percentile upper bounds using worker-local counters and avoids retaining and sorting every batch latency.
- Added `Node.PublishBatch` for up to 1,024 local-owner records and a 4 MiB budget. New journal frames share a sync. Stable IDs support retry after a partially written batch. Success does not provide an atomic multi-record transaction.
- Added opt-in dynamic topic names through a wildcard manifest, with independent partition offsets and topic-hashed placement. The mode is exclusive with named-topic declarations.
- Reduced per-stream allocations by creating wake channels and checkpoint maps lazily and storing the first producer ID inline. Added concurrent read locking.
- Added a reproducible burst, topic-cardinality, checkpointing and recovery workload through `make dev-burst`. Local measurements and limitations are recorded in `docs/burst-results.txt`.
- Added a mergeable, versioned HyperLogLog sketch with configurable precision and binary serialization for approximate distinct-count telemetry.
- Added `Node.Backup`, which copies and syncs a consistent journal into a new directory while holding the node lock.
- Added explicit per-partition `Node.Retain` with checks against existing consumer checkpoints, absolute offset floors, `ErrExpired` for old reads and stable directory locking. Retention rewrites and atomically replaces the journal. Producer IDs expire with deleted records.
- Added recovery of retained offsets and consumer state on reopen. Recovery scans the retained journal. Independent startup snapshots, an on-disk index, automatic retention scheduling, leases and delivery scheduling remain future work.
- Added tests for sketches, backup, batch retries, partial frames, notifications, concurrent access, retention, consumer checkpoints and reopening.
- **Storage-format and upgrade notes.** `Retain` replaces `journal.v1` with a new file. 0.2.x locks only `journal.v1` and does not know the replacement protocol, so as a second line of defence, while a 0.3.0 node runs, the replaced file stays open and locked (cut to one complete `DOST0002` fence frame, so its space is released and a stale 0.2.x descriptor reads a header it rejects) until the next `Retain` or `Close`, so a 0.2.x process that opened the old file just before a replacement cannot lock it. Upgrading is one way; keep a `Backup` taken by the old version before first running `Retain`.
- **Mixed-version limits.** Peers speak the single-record wire protocol, and a peer running 0.2.x does not know `ErrExpired`: a read below a retention floor reaches it as an unrecognised error rather than a distinct expiry. Upgrade all peers before enabling `Retain`, and treat `ErrExpired` as end of history, not as a transient error.
- **0.3.0 directories cannot be opened by 0.2.x.** New frames carry the signature `DOST0002`, which 0.2.x rejects with `invalid journal header` without modifying the file. A 0.2.x journal gets a marker frame the first time 0.3.0 opens it. Downgrading means restoring a backup taken before the upgrade. `sh scripts/fence-check.sh` proves this with an unmodified 0.2.3 binary.
- **Fail-closed journal.** A failed directory sync after a retention swap, or a failed write, stops further writes until the node is reopened. Recovery now always syncs the journal before serving.

## 0.4.0 — Publishing throughput, memory, an HTTP API and clients (26 November 2026)

Integrates the post-0.3.0 publishing work (ZIP SHA-256 `fb422713f1b5550fa8c30ae9d0265f13de4f6011b562ee4325a39608f9ad0ef6`, baseline candidate `cf0ed72c`) onto the reviewed 0.3.0 line. The journal format and the root package's exported API are unchanged from 0.3.0; 0.4.0 directories open exactly where 0.3.0 directories do. `clienthttp` and `wire` are new packages.

- **Group commit for concurrent `Node.Publish`.** An idle node writes inline. Calls that arrive during a write join a bounded queue (64 calls, 1 MiB of topic, key, ID and value bytes) and share one write and one sync. Every caller returns only after the sync that covers its record; overflow callers wait on the node lock. Per-call errors, cancellation before the write, conflicting retries and independent result buffers are tested. A crash can persist a prefix of a group; retry with the original IDs.
- **Gathering.** After a group is released, the drainer waits up to 100 microseconds for the callers it released before it takes the next group, so many producers keep sharing syncs. A lone producer never waits.
- **Linux `fdatasync` for journal appends.** `EINTR` is retried and an unsupported call falls back to a full `fsync`. File creation, compaction, backup, recovery and directory syncs keep full sync. Other platforms use `File.Sync`.
- **Lower memory per stream.** The first record location and producer ID are stored inline; the tail index, waiter and checkpoint state are allocated on demand. Single-owner and single-partition nodes skip owner and key hashing. Recovery reads through a bounded buffer and reuses its decode buffer; batches encode into one output buffer; single appends reuse a frame buffer capped at 16 KiB.
- **Retention rebuilds the index while copying.** Offsets and producer IDs are rebuilt from the replacement journal; there is no second read pass.
- **HTTP client API and gateway.** `clienthttp` is a standard-library `http.Handler` over a `Store` (publish, read with bounded long polling, position, commit) with binary payloads, bearer-token auth and typed status codes; `cmd/dostoevsky-gateway` serves one embedded node with it: loopback by default, a non-loopback address is refused unless TLS (`-tls-cert`, `-tls-key`) is set, and `-gen-token` prints a 32-byte random token. Clients for other languages are in the next entry.
- **Clients for five languages in four packages.** Python, JavaScript with TypeScript types (one package), Java and Rust clients (each dependency-free) with runnable examples, `make dev-client-*` targets, `make dev-client-interop` (all clients against one gateway across a restart), and `docs/clients.md` with a quickstart per language.
- **Benchmarks.** `make dev-bench-embedded`, `dev-bench-publish` and `dev-bench-publish-gate` run an isolated `benchmarks/embedded` module against embedded JetStream (Linux only). nats-server v2.15.3 and nats.go v1.56.0 appear only in that module's `go.mod`; the runtime module stays standard-library only. The release tarball includes the benchmark module. Results from a shared AMD64 host and from an ARM64 host (production-03) are in `docs/`. The benchmark module also gained further engines, per-language client workers, a gateway control and a fixed-window sustained mode; these are measurement tools and change nothing in the library.
- **Wire protocol codec; no server.** `docs/wire-v1.md` describes a native binary protocol, still a draft. The new `wire` package encodes and decodes version 1 for both sides within the protocol's bounds, with vector tests and fuzz targets. Nothing in this release serves the protocol.
- **TypeScript/JavaScript native client (preview).** `dostoevsky-client/native` speaks the wire protocol over TCP, TLS or a unix socket, with pipelined requests and client-side limits, and does not retry or reconnect. No released server speaks the protocol, so it is tested only against the spec vectors and a scripted server.
- **Known limits to weigh before production use.** One durable copy per partition. The journal and its in-memory index grow with retained history, and open replays it all. A crash can leave a frame that fails its checksum (out-of-order page writeback); `Open` then stops with the offset instead of truncating, there is no repair tool, and the way back is a backup. A torn final frame is truncated as before.
- **Integration notes.** Reviewed fixes from the 0.3.0 line are preserved on the new code paths: canonical journal path for repeated `Retain`, post-rename fail-closed journal, retired-inode fence frame, `DOST0002` signature and Fence marker, split retention seed frames and seed merging (`stream.seeded`), recovery sync of a clean tail, torn-V2-fence handling, hidden-sibling `Backup` and the `ErrIncompleteBackup` guard.

## 0.5.0 (unreleased)

- **TypeScript/JavaScript native client: random request ids.** A server is now very unlikely to guess a request's id before receiving its header. The README states what remains.
- **Breaking: upgrading a 0.2.x directory is explicit.** Until now `Open` marked such a journal on sight, after which 0.2.x refused it. `Open` now returns `ErrUpgradeRequired` and changes nothing; back up the directory, then run `dost-repair -data DIR -upgrade` (or call `UpgradeJournal`). Directories already opened by 0.3.0 or later, and new ones, are unaffected.
- **Repairing a damaged journal.** `Open` still stops at a frame that does not verify. New: `InspectJournal`, `RepairJournal` and the `dost-repair` command, which truncate after the last good frame only when asked, keep a copy of what they remove, and refuse to discard good frames that follow the damage without a second flag. `Node.Truncated` reports the torn final frame that `Open` removes, and the gateway logs it. `Open` no longer cuts a final frame whose announced length hides frames that verify: it refuses, as for other damage.
- **A cap on the journal.** `Node.SetJournalCap` and the gateway's `-max-journal-mib` refuse publishes with `ErrJournalFull` (also `ErrLimit`) rather than let the file pass a size, with a reserve for commits so `Retain` can make room. Off by default. `Node.JournalSize` reports the size.
- **Rust client.** Sets `TCP_NODELAY`, so a request body is sent without waiting for the server to acknowledge the request head.
- **Faster open.** `Open` reads the journal in 1 MiB pieces and takes the topic, partition, offset and ID straight from a record frame it recognises, instead of building the whole record with `encoding/json`. It checks the same things as before: every checksum, and that each value and time decode. Anything unrecognised is decoded in full. Open time still grows with the history.
- **`wireserver` and `dostoevsky-server`.** A server for the [native wire protocol, version 1](docs/wire-v1.md): token handshake, TLS or loopback and unix sockets only, ordered publish and commit with queued publishes sharing a sync, read with bounded waiting, position, ping, per-connection window, server-wide request and reply budgets, connection limits and absolute deadlines. Standard library only. No speed or memory figure is claimed: none has been measured.
- **`Node.Await`.** Waits until an offset can be read, without reading it.
