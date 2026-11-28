#!/usr/bin/env python3
"""Runs one trace of requests through two or more engines and compares what they wrote, byte for byte.

    python3 scripts/differential.py --engine go=PATH/TO/dost-conform --engine rust=PATH [--trace NAME] [--format F]

Standard library only. The engines are driven through the conformance drivers (cmd/dost-conform/README.md), one
request at a time, because group commit under concurrency depends on timing and is the conformance suite's business.

Each engine runs every trace in its own new directory, which is also its working directory: a trace names its stores by
relative paths, so every engine receives the same request bytes. With the test seam (the `fix_inputs` request) the clock
and the random source are fixed first, and two engines that agree write identical trees and give identical replies.

Compared, against the first engine named:
  replies   as parsed JSON with "message" removed (the suite compares error codes, never their text); the number of
            reply lines that are also identical as bytes is reported beside it
  trees     the set of paths, each file's bytes, and each symbolic link's target

Exit status: 0 identical, 1 different, 2 a driver or usage failure. The trace format is in rust/differential/README.md.
"""
import argparse
import base64
import glob
import hashlib
import json
import os
import subprocess
import sys
import tempfile

TRACES = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "rust", "differential", "traces")
TIMEOUT = 120
FORMATS = ("legacy", "segmented")
INPUTS = {"start": 1790000000000000000, "step": 1000, "seed": "differential"}


class Broken(Exception):
    """A driver could not run the trace at all: not a difference between engines."""


def text(value):
    return base64.b64encode(value.encode()).decode()


def requests_of(trace, fmt):
    """The trace as the list of driver requests for one format, with the conveniences of the case files."""
    out = []
    for step in trace["requests"]:
        if step.get("only", fmt) != fmt:
            continue
        request = {k: v for k, v in step.items() if k != "only"}
        if request["op"] == "open":
            request.setdefault("dir", "store")
            request.setdefault("segmented", fmt == "segmented")
            request.setdefault("node", "a")
            request.setdefault("nodes", ["a"])
            request.setdefault("topics", {"events": 1})
        if request["op"] in ("publish", "read", "read_batch", "position", "commit_batch"):
            request.setdefault("topic", "events")
        for key in ("value", "state"):
            if key in request:
                request[key] = text(request[key])
        if "items" in request:
            request["items"] = [dict(m, value=text(m.get("value", "")), topic=m.get("topic", "events")) for m in request["items"]]
        if "floors" in request:
            request["floors"] = [dict({"topic": "events", "partition": 0}, **f) for f in request["floors"]]
        for key in ("dir", "dest"):
            if key in request and os.path.isabs(request[key]):
                raise Broken("a trace must name stores by relative paths, not %s" % request[key])
        out.append(request)
    return out


def run(name, binary, requests, root, inputs):
    """Sends the requests one at a time. Returns the reply lines as bytes."""
    process = subprocess.Popen([os.path.abspath(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, cwd=root)
    replies = []
    try:
        if inputs is not None:
            requests = [dict({"op": "fix_inputs"}, start=inputs["start"], step=inputs["step"], seed=text(inputs["seed"]))] + requests
        for number, request in enumerate(requests):
            process.stdin.write(json.dumps(request).encode() + b"\n")
            process.stdin.flush()
            line = process.stdout.readline()
            if not line:
                status = process.wait(TIMEOUT)
                raise Broken("the %s driver stopped answering at request %d (%s), status %s" % (name, number, request["op"], status))
            if inputs is not None and number == 0:
                if not json.loads(line)["ok"]:
                    raise Broken("the %s driver refused fix_inputs: %s" % (name, line.decode().strip()))
                continue
            replies.append(line)
        process.stdin.close()
        status = process.wait(TIMEOUT)
        if status != 0:
            raise Broken("the %s driver exited with status %d" % (name, status))
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
    return replies


def tree(root):
    """Every path under root: 'dir', ('link', target) or ('file', bytes)."""
    found = {}
    for directory, names, files in os.walk(root):
        for entry in names + files:
            path = os.path.join(directory, entry)
            relative = os.path.relpath(path, root)
            if os.path.islink(path):
                found[relative] = ("link", os.readlink(path))
            elif os.path.isdir(path):
                found[relative] = "dir"
            else:
                with open(path, "rb") as f:
                    found[relative] = ("file", f.read())
    return found


def comparable(line):
    reply = json.loads(line)
    reply.pop("message", None)
    return reply


def first_difference(a, b):
    for i, (x, y) in enumerate(zip(a, b)):
        if x != y:
            return i
    return min(len(a), len(b))


def compare(base, other, requests):
    """The differences of `other` from `base`, as lines of text, and counts for the receipt."""
    (base_name, base_replies, base_tree), (name, replies, other_tree) = base, other
    differences = []
    if len(replies) != len(base_replies):
        differences.append("replies: %s gave %d, %s gave %d" % (base_name, len(base_replies), name, len(replies)))
    same_bytes = 0
    for number, (x, y) in enumerate(zip(base_replies, replies)):
        same_bytes += x == y
        if comparable(x) != comparable(y):
            differences.append("reply %d (%s): %s %s | %s %s" % (number, requests[number]["op"], base_name, x.decode().strip()[:160], name, y.decode().strip()[:160]))
    for path in sorted(set(base_tree) | set(other_tree)):
        x, y = base_tree.get(path), other_tree.get(path)
        if x is None or y is None:
            differences.append("tree: %s exists only for %s" % (path, name if x is None else base_name))
        elif x != y:
            if isinstance(x, tuple) and isinstance(y, tuple) and x[0] == y[0] == "file":
                differences.append("tree: %s differs at byte %d (%s has %d bytes, %s has %d)" % (path, first_difference(x[1], y[1]), base_name, len(x[1]), name, len(y[1])))
            else:
                differences.append("tree: %s is not the same kind of entry, or links elsewhere" % path)
    files = [v for v in base_tree.values() if isinstance(v, tuple) and v[0] == "file"]
    counts = {"requests": len(requests), "replies_equal_ignoring_message": sum(comparable(x) == comparable(y) for x, y in zip(base_replies, replies)),
              "replies_identical_bytes": same_bytes, "paths": len(base_tree), "files": len(files), "file_bytes": sum(len(v[1]) for v in files),
              "tree_sha256": {n: digest(t) for n, t in ((base_name, base_tree), (name, other_tree))}, "differences": len(differences)}
    return differences, counts


def digest(found):
    h = hashlib.sha256()
    for path in sorted(found):
        entry = found[path]
        h.update(path.encode() + b"\0")
        h.update(entry.encode() if entry == "dir" else entry[0].encode() + b"\0" + (entry[1] if entry[0] == "file" else entry[1].encode()))
        h.update(b"\0")
    return h.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--engine", action="append", required=True, metavar="NAME=PATH", help="a driver; the first is the reference; name one twice to compare an engine with itself")
    parser.add_argument("--trace", action="append", help="run only this trace (the file name without .json)")
    parser.add_argument("--format", action="append", choices=FORMATS, help="run only this storage format")
    parser.add_argument("--without-seam", action="store_true", help="do not send fix_inputs: for a driver that has no test seam yet")
    parser.add_argument("--receipt", help="write the counts of every comparison to this file, as JSON")
    parser.add_argument("--keep", help="keep the engines' directories under this directory")
    args = parser.parse_args()
    engines = [tuple(e.split("=", 1)) for e in args.engine]
    if len(engines) < 2 or any(len(e) != 2 for e in engines):
        sys.exit("give at least two --engine NAME=PATH")
    names = sorted(os.path.basename(p)[:-5] for p in glob.glob(os.path.join(TRACES, "*.json")))
    for wanted in args.trace or []:
        if wanted not in names:
            sys.exit("no trace named %s (have %s)" % (wanted, ", ".join(names)))
    receipt, different, broken, ran = [], 0, 0, 0
    for trace_name in args.trace or names:
        with open(os.path.join(TRACES, trace_name + ".json")) as f:
            trace = json.load(f)
        if set(trace) != {"about", "formats", "requests"} or any(f not in FORMATS for f in trace["formats"]) or not trace["requests"]:
            sys.exit("trace %s: want about, formats (%s) and requests" % (trace_name, ", ".join(FORMATS)))
        for fmt in trace["formats"]:
            if args.format and fmt not in args.format:
                continue
            ran += 1
            label = "%s [%s]" % (trace_name, fmt)
            holder = tempfile.mkdtemp(prefix="differential-", dir=args.keep) if args.keep else None
            with tempfile.TemporaryDirectory(prefix="differential-") as scratch:
                top = holder or scratch
                try:
                    requests = requests_of(trace, fmt)
                    results = []
                    for index, (name, binary) in enumerate(engines):
                        root = os.path.join(top, "%d-%s" % (index, name))
                        os.mkdir(root)
                        replies = run(name, binary, requests, root, None if args.without_seam else INPUTS)
                        results.append((name, replies, tree(root)))
                except Broken as e:
                    broken += 1
                    print("BROKEN  %s: %s" % (label, e))
                    receipt.append({"trace": trace_name, "format": fmt, "broken": str(e)})
                    continue
            for other in results[1:]:
                differences, counts = compare(results[0], other, requests)
                receipt.append(dict({"trace": trace_name, "format": fmt, "reference": results[0][0], "engine": other[0], "seam": not args.without_seam}, **counts))
                if differences:
                    different += 1
                    print("DIFFER  %s: %s against %s, %d differences" % (label, other[0], results[0][0], len(differences)))
                    for line in differences[:12]:
                        print("        " + line)
                    if len(differences) > 12:
                        print("        ... and %d more" % (len(differences) - 12))
                else:
                    print("same    %s: %s against %s: %d replies (%d identical as bytes), %d files, %d bytes" % (
                        label, other[0], results[0][0], counts["requests"], counts["replies_identical_bytes"], counts["files"], counts["file_bytes"]))
    if args.receipt:
        with open(args.receipt, "w") as f:
            json.dump(receipt, f, indent=1)
            f.write("\n")
    if not ran:
        sys.exit("nothing ran")
    print("%d comparisons: %d different, %d broken" % (len(receipt), different, broken))
    sys.exit(2 if broken else 1 if different else 0)


if __name__ == "__main__":
    main()
