"""Validate published text hashes, privacy screen and shared linked versions."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
from export_incremental import ROOT, DEST, screen


def main():
    ap=argparse.ArgumentParser();ap.add_argument("--staged",action="store_true");ap.add_argument("--bin",type=Path)
    args=ap.parse_args()
    def content(path):
        if args.staged:return subprocess.check_output(["git","show",":"+path],cwd=ROOT)
        return (ROOT/path).read_text(encoding="utf-8").replace("\r\n","\n").encode()
    manifest=json.loads(content((DEST/"manifest.json").relative_to(ROOT).as_posix()))
    entries=manifest["evidence"]+manifest["source"]
    for entry in entries:
        path=entry["path"]
        if not (ROOT/path).resolve().is_relative_to(ROOT):raise ValueError("path-outside-repository")
        data=content(path)
        if len(data)!=entry["bytes"] or hashlib.sha256(data).hexdigest()!=entry["sha256"]:
            raise ValueError("manifest-mismatch: "+path)
        if entry in manifest["evidence"]:screen(json.loads(data))
    build=json.loads(content((DEST/"comparison-build.json").relative_to(ROOT).as_posix()))
    def versions(name):
        rows=[x.split() for x in build[name]["metadata"]]
        return {x[1]:x[2] for x in rows if len(x)>2 and x[0] in ("dep","mod") and x[2]!="(devel)"}
    embedded,core=versions("B.exe"),versions("core.exe")
    shared=embedded.keys() & core.keys()
    if any(embedded[k]!=core[k] for k in shared):raise ValueError("linked-version-mismatch")
    if args.bin:
        for name,item in build.items():
            data=(args.bin/name).read_bytes()
            if len(data)!=item["bytes"] or hashlib.sha256(data).hexdigest()!=item["sha256"]:
                raise ValueError("binary-mismatch")
    # Final Go/module inputs were committed after building; do not mislabel the
    # embedded dirty VCS revision as the final source commit.
    source_revision="d5e066c"
    paths=subprocess.check_output(["git","ls-files","experiments/runtime-mihomo"],cwd=ROOT,text=True).splitlines()
    checked=0
    for path in paths:
        if not (path.endswith(".go") or path.endswith("go.mod") or path.endswith("go.sum")):continue
        if content(path)!=subprocess.check_output(["git","show",source_revision+":"+path],cwd=ROOT):
            raise ValueError("evaluated-Go-input-changed")
        checked+=1
    print(json.dumps({"hashBasis":"git-index-blobs" if args.staged else "normalized-worktree",
        "entriesVerified":len(entries),"evidenceFiles":len(manifest["evidence"]),
        "sharedLinkedVersionsMatched":len(shared),"GoAndModuleFilesMatchingEvaluatedCommit":checked,
        "binaryBytesVerified":bool(args.bin),"privacyScreenPassed":True}))


if __name__=="__main__":main()
