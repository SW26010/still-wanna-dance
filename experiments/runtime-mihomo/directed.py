"""Finite, loopback-only follow-up. Never loads a private subscription.

Each runtime generation has a separate owned process. All phases and failures
are saved under a fresh ignored directory; exporting evidence is explicit.
"""
import argparse
import hashlib
import json
from pathlib import Path
import secrets
import subprocess
import sys
import time

sys.dont_write_bytecode = True
import run as r
m = r.m


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", type=Path, required=True)
    args = ap.parse_args()
    args.bin = args.bin.resolve()
    root = args.bin / ("hot-" + secrets.token_hex(5))
    m.private_dir(root)
    hosts = Path(m.os.environ["SystemRoot"]) / "System32/drivers/etc/hosts"
    before = m.sha(hosts)
    out = {"scope": "loopback development; no product migration or architecture selection",
           "baseRevision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=r.ROOT, text=True).strip(),
           "checks": [], "scenarios": [], "binaries": {}}
    for name in ("B.exe", "hotfixture.exe"):
        out["binaries"][name] = {"sha256": m.sha(args.bin / name), "bytes": (args.bin / name).stat().st_size}
    owned = []
    fixture_job = fixture = probe = None
    try:
        for scenario, limit in (("cancel", 128), ("retire", 128), ("credit", 2)):
            manifest = root / (scenario + "-private.json")
            fixture_job = m.Job()
            fixture = fixture_job.start([str(args.bin / "hotfixture.exe"), str(manifest), str(limit)],
                                        stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                        env=m.clean_env())
            owned.append(fixture)
            deadline = time.monotonic() + 5
            while not manifest.exists() and time.monotonic() < deadline:
                if fixture.poll() is not None:
                    raise RuntimeError("fixture-exited")
                time.sleep(.02)
            private = json.loads(manifest.read_text(encoding="utf-8"))
            home = root / (scenario + "-home")
            home.mkdir()
            probe = r.Probe(args.bin / "B.exe", {"Mode": "B2", "Payload": json.dumps({"proxies": [private["node"]]}),
                                                "Home": str(home), "DNS": "127.0.0.1:1"})
            owned.append(probe.p)
            if not probe.ready.get("ready"):
                raise RuntimeError("probe-not-ready")
            result = probe.ask({"Directed": scenario, "ControlURL": private["control"]})
            out["scenarios"].append({"scenario": scenario, "peerMaxIncomingStreams": limit, **result})
            for row in result["checks"]:
                out["checks"].append({"name": scenario + "/" + row["name"], "passed": row["passed"]})
                print(scenario, row["name"], "PASS" if row["passed"] else "FAIL", flush=True)
            probe.close()
            probe = None
            # Retirement recovery is a new generation, never reuse the retired
            # object's identity or reset Mihomo's process-global initialization.
            if scenario == "retire":
                home = root / "replacement-home"
                home.mkdir()
                probe = r.Probe(args.bin / "B.exe", {"Mode": "B2", "Payload": json.dumps({"proxies": [private["node"]]}),
                                                    "Home": str(home), "DNS": "127.0.0.1:1"})
                owned.append(probe.p)
                rows = probe.request()
                ok = len(rows) == 1 and rows[0]["ok"] and rows[0]["bytes"] == 2
                out["checks"].append({"name": "retire/replacement-generation-recovers", "passed": ok})
                out["replacement"] = rows
                print("retire replacement-generation-recovers", "PASS" if ok else "FAIL", flush=True)
                with m.urllib.request.build_opener(m.urllib.request.ProxyHandler({})).open(private["control"] + "/stats", timeout=1) as response:
                    state = json.load(response)
                out["replacementPeer"] = state
                ok = state["handshakes"] == 2
                out["checks"].append({"name": "retire/replacement-has-new-handshake", "passed": ok})
                print("retire replacement-has-new-handshake", "PASS" if ok else "FAIL", flush=True)
                probe.close()
                probe = None
            fixture.stdin.close()
            fixture.wait(timeout=5)
            fixture_job.close()
            fixture_job = fixture = None
    except BaseException as exc:
        out["fatal"] = type(exc).__name__
        print("RUNNER", type(exc).__name__, flush=True)
    finally:
        if probe:
            probe.close()
        if fixture_job:
            fixture_job.close()
        for process in owned:
            process.wait(timeout=5)
        out["cleanup"] = {"allOwnedProcessesExited": all(p.poll() is not None for p in owned),
                          "hostsUnchanged": before == m.sha(hosts)}
        (root / "results.json").write_text(json.dumps(out, indent=2) + "\n", encoding="utf-8")
        print("RESULT", root.relative_to(r.ROOT).as_posix(), flush=True)
    return int(bool(out.get("fatal")) or not out["checks"] or not all(c["passed"] for c in out["checks"]))


if __name__ == "__main__":
    sys.exit(main())
