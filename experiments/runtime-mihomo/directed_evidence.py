"""Publish/verify one immutable directed experiment evidence set."""
import argparse
import json
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
from export_incremental import ROOT, digest, screen, write


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--label", choices=("hot", "retry"), required=True)
    ap.add_argument("--result", type=Path)
    ap.add_argument("--tests", type=Path, action="append", default=[])
    ap.add_argument("--history", type=Path, action="append", default=[])
    ap.add_argument("--binary", type=Path, action="append", default=[])
    ap.add_argument("--go")
    ap.add_argument("--source", action="append", default=[])
    ap.add_argument("--verify-ref", help="Git revision or : for staged blobs")
    args = ap.parse_args()
    dest = ROOT / "docs/evidence/issue14-directed-20261013" / args.label
    if args.verify_ref:
        manifest = json.loads((dest / "manifest.json").read_text(encoding="utf-8"))
        for entry in manifest["evidence"] + manifest["source"]:
            spec = (":" if args.verify_ref == ":" else args.verify_ref + ":") + entry["path"]
            data = subprocess.check_output(["git", "show", spec], cwd=ROOT)
            if digest(data) != entry["sha256"] or len(data) != entry["bytes"]:
                raise ValueError("hash mismatch: " + entry["path"])
        print("Verified", len(manifest["evidence"] + manifest["source"]), "Git blobs")
        return
    if dest.exists():
        raise ValueError("refusing to overwrite evidence")
    result = json.loads(args.result.read_text(encoding="utf-8"))
    screen(result)
    tests = []
    for log in args.tests:
        for line in log.read_text(encoding="utf-8-sig").splitlines():
            event = json.loads(line)
            if event.get("Action") in ("pass", "fail", "skip"):
                tests.append({"run": log.stem, **{k: event[k] for k in ("Action", "Package", "Test", "Elapsed") if k in event}})
    identity = {}
    for binary in args.binary:
        raw = subprocess.check_output([args.go, "version", "-m", str(binary)], text=True)
        identity[binary.name] = {"sha256": digest(binary.read_bytes()), "bytes": binary.stat().st_size,
                                 "metadata": [s.strip() for s in raw.splitlines()[1:]]}
    screen(tests)
    screen(identity)
    history = [json.loads(p.read_text(encoding="utf-8")) for p in args.history]
    screen(history)
    dest.mkdir(parents=True)
    write(dest / "results.json", result)
    write(dest / "tests.json", tests)
    write(dest / "build.json", identity)
    if history:
        write(dest / "history.json", history)
    def entry(path):
        data = path.read_text(encoding="utf-8").replace("\r\n", "\n").encode()
        return {"path": path.relative_to(ROOT).as_posix(), "bytes": len(data), "sha256": digest(data)}
    write(dest / "manifest.json", {"hashBasis": "git-blob UTF-8 LF", "binaryHashBasis": "actual file bytes",
          "baseRevision": result["baseRevision"], "evidence": [entry(p) for p in sorted(dest.glob("*.json"))],
          "source": [entry(ROOT / path) for path in sorted(set(args.source))],
          "redaction": "Only loopback fixture categories/counts/timings, test outcomes and build identity; no raw node/control URLs or credentials."})
    print("Published", args.label)


if __name__ == "__main__":
    main()
