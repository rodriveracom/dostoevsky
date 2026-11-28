# The Rust engine

Dostoevsky is being rebuilt in Rust, in two phases. Phase 1, here, is a faithful port of the Go engine in the
repository root: module by module, the same bytes on disk and on the wire, the same error codes. The Go engine keeps
shipping until the Rust engine passes every gate.

**State: row 0.** The layout and the test drivers exist. No engine code is ported yet.

| Read | For |
| --- | --- |
| `PORT-NOTES.md` | the map from Go files to Rust modules, the Go behaviors kept or changed, the open questions |
| `DEPS.md` | every outside crate (none yet) and our own code where a crate would be usual |
| `differential/README.md` | the differential driver and its traces |
| `testdata/README.md` | the vectors written by other implementations, and how to write them again |
| `receipts/` | the output of each row's gates |

## Crates

| Crate | Holds |
| --- | --- |
| `dostoevsky-wire` | the codec, no I/O |
| `dostoevsky-format` | frames, sidecars and the journal encoding, no I/O; the Go-compatible JSON reader and writer |
| `dostoevsky-core` | the store; the test seam; the error kinds |
| `dostoevsky-workflow` | the workflow layer |
| `dostoevsky-server` | the server and the HTTP gateway |
| `dostoevsky-cli` | the command-line tools and the conformance driver |
| `dostoevsky-py`, `dostoevsky-node` | the bindings (row 14) |

## Gates

From the repository root:

```sh
make rust-check          # cargo fmt --check, cargo clippy -D warnings, cargo test
make conformance-rust    # every case, each engine opening the other's stores
make differential        # the same requests to both engines; the stores and replies must be equal
```

Build output goes to `.dev/rust-target`. The earlier prototype in `benchmarks/rustcore` is a frozen record: it is not
part of this workspace and these gates do not touch it.
