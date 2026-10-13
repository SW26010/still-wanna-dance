"""Bounded Windows CPU experiment. Run after starting trace-helper.ps1 via UAC."""
import argparse
import hashlib
import json
from pathlib import Path
import random
import subprocess
import time

LEVELS = [1, 4, 8, 12, 16, 24, 32]

def plan():
    rng = random.Random(20261014)
    rows = []
    for round_no in range(1, 4):
        arms = [("burst", n) for n in LEVELS] + [("stagger", n) for n in LEVELS[1:]] + [("stepped", 32)]
        rng.shuffle(arms)
        rows.append([dict(id=f"r{round_no}-idle", kind="idle", n=1)] + [dict(id=f"r{round_no}-{kind}-{n}", kind=kind, n=n) for kind, n in arms])
    return rows

def trace(root, action, stem=""):
    request_id = f"{action}-{time.monotonic_ns()}"
    tmp = root / "request-tmp.json"
    tmp.write_text(json.dumps(dict(id=request_id, action=action, stem=stem)), encoding="utf-8")
    tmp.replace(root / "trace-request.json")
    deadline = time.monotonic() + 50
    expected = dict(start="recording", stop="stopped", quit="exited")[action]
    while time.monotonic() < deadline:
        status = json.loads((root / "trace-status.json").read_text(encoding="utf-8-sig"))
        if status["state"] == "error":
            raise RuntimeError("ETW helper error; inspect private status file")
        if status["id"] == request_id and status["state"] == expected:
            return
        time.sleep(.25)
    raise RuntimeError("ETW helper acknowledgement timed out")

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--root", type=Path, default=Path("artifacts/cf-cpu"))
    p.add_argument("--round", type=int, choices=[1, 2, 3], required=True)
    args = p.parse_args()
    root = args.root.resolve()
    exe = root / "cpu-probe.exe"
    rows = plan()
    (root / "plan.json").write_text(json.dumps(dict(seed=20261014, holdSeconds=6, staggerMS=250, trialCapMiB=2048, batchCapMiB=20480, orders=rows, exeSHA256=hashlib.sha256(exe.read_bytes()).hexdigest()), indent=2)+"\n")
    stem = f"cpu-main-r{args.round}"
    for arm in rows[args.round-1]:
        if (root / (arm["id"]+".json")).exists():
            raise RuntimeError("Refusing to replace an existing trial")
    started = False
    total = sum(sum(o["bytes"] for o in json.loads(f.read_text())["requests"] or []) for f in root.glob("r*-*.json"))
    try:
        trace(root, "start", stem)
        started = True
        for arm in rows[args.round-1]:
            if total >= 18*1024**3:
                raise RuntimeError("Batch traffic reserve exhausted; stopping below 20 GiB")
            time.sleep(1)
            out = root / (arm["id"]+".json")
            command = [str(exe), "-mode", "cpu", "-private", str(root/"target.json"), "-ips", "104.26.4.28", "-cpu-reference", str(root/"reference.json"), "-cpu-kind", arm["kind"], "-cpu-n", str(arm["n"]), "-cpu-hold", "6s", "-cpu-id", arm["id"], "-out", str(out)]
            subprocess.run(command, check=True, timeout=70)
            result = json.loads(out.read_text())
            total += sum(o["bytes"] for o in result["requests"] or [])
            if not result["ok"]:
                raise RuntimeError("Failed trial retained; stopping batch")
        print(f"round={args.round} accumulated_MiB={total/1024**2:.0f}", flush=True)
    finally:
        if started:
            trace(root, "stop")

if __name__ == "__main__":
    main()
