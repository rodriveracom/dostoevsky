# Outside crates

Rule: every outside crate is listed here with its version, its registry link and the reason for it, and is at least 14
days old when it is pinned.

## At row 0: none

The workspace builds from the standard library alone. `Cargo.lock` lists only the workspace's own crates.

## Expected, not yet added

No registry could be reached from the machine row 0 was built on, so no version has been chosen and no age has been
checked. Each crate is added, with its version, link and release date, in the row that first needs it.

| Crate | Needed for | First row |
| --- | --- | ---: |
| `tokio` | the server's network I/O; phase 2 work on the engine | 11 |
| `rustls` | TLS in the server and the gateway | 11 |
| `sha2` | see below | 0, if chosen |
| `pyo3` | the Python binding | 14 |
| `napi`, `napi-derive` | the Node addon | 14 |
| `wasm-bindgen` | WebAssembly builds of the two crates without I/O | 14 |

## Our own code where a crate would be usual

| Code | Where | Why not a crate |
| --- | --- | --- |
| JSON reader and writer | `dostoevsky-format::gojson` | The format is the output of Go's encoder, byte for byte; it has to be a hand-checked writer |
| base64 | `dostoevsky-format::base64` | Part of the same Go-compatible encoding; 80 lines |
| CRC-32C | `dostoevsky-format`, from row 2 | Carried over from the earlier prototype |
| SHA-256 | `dostoevsky-core::sha256` | **To be replaced by the `sha2` crate**, which could not be fetched here. This file is used by the test seam only, is checked against the standard vectors and 200 digests from another implementation, and can be replaced by the crate in one commit |
