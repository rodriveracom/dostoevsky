# Row 0 receipts

| File | What it shows |
| --- | --- |
| `environment.txt` | machine, OS, toolchains, the commit of `main` |
| `rust-check.txt` | `cargo fmt --check` and `cargo clippy -D warnings` exit 0; 16 unit tests pass |
| `conformance.txt` | the Go engine alone passes the suite; with the Rust driver every pairing that opens a store with it fails at `open`, naming the row that will bring it |
| `differential-go-go-without-seam.txt`, `.json` | the Go engine against itself, without the seam: all 6 comparisons differ |
| `differential-go-rust.txt` | Go against Rust with the seam: the Go driver on `main` has no `fix_inputs` and stops; without the seam every reply differs, as it must before any engine code exists; the Rust driver against itself is identical |

The per-row gates (conformance cases for the row, byte-identical differential, cross-open) have nothing to pass in
row 0: there is no engine code. What row 0 can show is that the drivers run and report correctly.
