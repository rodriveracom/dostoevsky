# Vectors from other implementations

Each file here was written by another implementation and is checked by a unit test. None is edited by hand.

| File | Written by | Checks |
| --- | --- | --- |
| `gojson-strings.txt` | `go run rust/testdata/gojson_vectors.go` | `gojson::write_string`, with and without HTML escaping: 2,075 strings |
| `gojson-reader.txt` | `go run rust/testdata/gojson_reader_vectors.go` | `gojson::parse`: 851 inputs Go refuses, 724 strings it decodes, 20 other values it accepts |
| `sha256-lengths.txt` | Python's `hashlib` | `sha256::digest` at 200 lengths around the block boundaries |
| `seam-random.txt` | Python's `hashlib` | the first four blocks of the fixed random source |

The two Go programs carry `//go:build ignore`, so the Go module does not build them.

**They were written with Go 1.24.7.** The format is defined as Go 1.27.0's output. Write them again with 1.27.0 and
rerun `cargo test`: the header of `gojson-strings.txt` records which form the toolchain writes for invalid UTF-8, and
the test follows it.
