# The differential test

`scripts/differential.py` sends one trace of requests to two or more engines and compares what they wrote, byte for
byte. It is the check behind "the same on-disk bytes" in phase 1.

```sh
make differential
python3 scripts/differential.py --engine go=GO_DRIVER --engine rust=RUST_DRIVER [--trace NAME] [--format legacy]
python3 scripts/differential.py --engine go=GO_DRIVER --engine again=GO_DRIVER --without-seam   # an engine against itself
```

Linux, Python 3.10 or later, standard library only. The engines are driven through their conformance drivers
(`cmd/dost-conform/README.md`), so nothing is translated twice.

## How it works

- **One request at a time.** Group commit under concurrency depends on timing. Concurrent behavior belongs to the
  conformance suite, which compares state, not bytes.
- **The same request bytes for every engine.** Each engine runs a trace in its own new directory, which is also its
  working directory, and a trace names its stores by relative paths (`store`, `copy`). An absolute path is refused.
- **Fixed inputs.** Before the trace, the runner sends `fix_inputs`, which sets the engine's clock and random source
  (the test seam). `--without-seam` leaves that out, for a driver that has no seam yet; then even one engine run twice
  differs, in every record time at least.
- **What is compared,** against the first engine named:
  - replies, as parsed JSON with `message` removed, because the suite compares error codes and never their text; the
    number of reply lines that are also identical as bytes is reported beside it;
  - the two directory trees: the set of paths, each file's bytes, each symbolic link's target.
- **Exit status:** 0 identical, 1 different, 2 a driver could not run the trace.
- `--receipt FILE` writes the counts of every comparison as JSON. `--keep DIR` keeps the directories for inspection.

## Traces

`traces/*.json`: `about`, `formats` (`legacy`, `segmented`) and `requests`. A request is a driver request, with the same
conveniences as the conformance case files: `open` gets a default directory, format, node and topics; `value` and
`state` are written as text; `topic` defaults to `events`. `only` limits a request to one format.

| Trace | Requests (legacy, segmented) | What it exercises |
| --- | ---: | --- |
| `publish-read-commit` | 24, 25 | every record field, a repeated ID, a batch across topics, reads, commits with state, a reopen |
| `retention` | 15, 15 | retention refused above a group's position, retention twice, an ID reused after retention, a reopen |
| `backup-and-migrate` | 16, 11 | a backup, a backup onto an existing directory, migration to the segmented format, inspection |

Requests that must fail (an unknown topic, a stale commit, a read past the end) are part of the traces: the error codes
are compared too.

## Limits

- It compares stores and driver replies. Comparing wire bytes needs the server (row 11) and is not built.
- The three traces are a start, not coverage. Each row adds traces for what it ports.
- The seam requests are provisional until the Go driver has them (`../PORT-NOTES.md`, open question 1). Against the Go
  driver on `main` the runner stops at `fix_inputs`.
