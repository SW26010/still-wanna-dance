"""Small B1/B2 retry attribution; deterministic peer failures, no backlog race."""
import argparse
import http.server
import json
from pathlib import Path
import secrets
import subprocess
import sys
import threading
import time

sys.dont_write_bytecode = True
import run as r
m = r.m


class Attempts(m.TCPServer):
    request_queue_size = 128

    @property
    def fail(self):
        with self.lock:
            self.attempts += 1
            reject = self.reject < 0 or self.attempts <= self.reject
            if reject and self.cut:
                # Existing handler catches EOFError and closes before sending
                # a reply: native Dial now observes a handshake error.
                raise EOFError()
            return reject


class Stall(m.socketserver.BaseRequestHandler):
    def handle(self):
        with self.server.lock:
            self.server.attempts += 1
            self.server.active += 1
        try:
            self.request.settimeout(8)
            while self.request.recv(4096):
                pass
        except OSError:
            pass
        finally:
            with self.server.lock:
                self.server.active -= 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", type=Path, required=True)
    args = ap.parse_args()
    args.bin = args.bin.resolve()
    root = args.bin / ("retry-" + secrets.token_hex(5))
    m.private_dir(root)
    hosts = Path(m.os.environ["SystemRoot"]) / "System32/drivers/etc/hosts"
    before = m.sha(hosts)
    out = {"baseRevision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=r.ROOT, text=True).strip(),
           "scope": "controlled SOCKS failures; no inferred attribution of historical backlog results",
           "checks": [], "cases": [], "binaries": {"B.exe": {"sha256": m.sha(args.bin / "B.exe")}}}
    probes, processes, servers = [], [], []
    def record(name, passed, **data):
        out["checks"].append({"name": name, "passed": bool(passed), **data})
        print(name, "PASS" if passed else "FAIL", flush=True)
    def peer(reject=0, stall=False, cut=False):
        http_peer = m.background(http.server.ThreadingHTTPServer(("127.0.0.1", 0), m.HTTPFixture))
        http_peer.marker, http_peer.hits, http_peer.streaming = "A", 0, threading.Event()
        servers.append(http_peer)
        s = Attempts(("127.0.0.1", 0), Stall if stall else m.SocksFixture)
        s.http, s.lock, s.attempts, s.reject, s.targets, s.active = http_peer, threading.Lock(), 0, reject, [], 0
        s.cut = cut
        servers.append(m.background(s))
        return s
    def start(mode, s):
        home = root / (mode + "-" + secrets.token_hex(4))
        home.mkdir()
        p = r.Probe(args.bin / "B.exe", {"Mode": mode, "Home": str(home), "DNS": "127.0.0.1:1", "DialTimeoutMS": 5000,
                     "Payload": m.socknode("fixture", s.server_address[1], "127.0.0.1").decode()})
        probes.append(p)
        processes.append(p.p)
        if not p.ready.get("ready"):
            raise RuntimeError("probe-not-ready")
        return p
    try:
        for mode in ("B", "B2"):
            for injection, cut in (("reply-code-5", False), ("handshake-eof", True)):
                native_retry = mode == "B" and cut
                s = peer(reject=1, cut=cut)
                p = start(mode, s)
                first = p.request(TimeoutMS=6000)
                attempts = s.attempts
                record(mode + "-first-" + injection, len(first) == 1 and first[0]["ok"] == native_retry
                       and attempts == (2 if native_retry else 1), attempts=attempts, requests=first)
                if not native_retry:
                    second = p.request(TimeoutMS=6000)
                    record(mode + "-next-request-after-" + injection, len(second) == 1 and second[0]["ok"] and s.attempts == 2,
                           attempts=s.attempts, requests=second)
                p.close()
                s = peer(reject=-1, cut=cut)
                p = start(mode, s)
                rows = p.request(TimeoutMS=6000)
                p.close()  # snapshot peer attempts only after all owned work ends
                record(mode + "-persistent-" + injection, len(rows) == 1 and not rows[0]["ok"]
                       and (2 <= s.attempts <= 10 if native_retry else s.attempts == 1), attempts=s.attempts, requests=rows)
            if mode == "B2":
                s = peer(reject=-1)
                p = start(mode, s)
                rows = p.request(RawDial=True, Address="business.invalid:80", TimeoutMS=1000)
                record("B2-native-SOCKS-reply-code-not-Dial-error", len(rows) == 1 and rows[0]["ok"] and s.attempts == 1,
                       attempts=s.attempts, requests=rows)
                p.close()
            s = peer(stall=True)
            p = start(mode, s)
            rows = p.request(PreCancel=True)
            record(mode + "-pre-cancel-never-starts-peer", len(rows) == 1 and not rows[0]["ok"] and s.attempts == 0)
            start_time = time.monotonic()
            rows = p.request(TimeoutMS=100)
            immediate = p.ask({"Stats": True})
            pending = immediate.get("admission", immediate)["pending"]
            record(mode + "-HTTP-cancel-can-leave-dial-running", len(rows) == 1 and not rows[0]["ok"]
                   and rows[0]["elapsedMS"] < 1000 and pending > 0, requests=rows, state=immediate)
            final = immediate
            while time.monotonic() - start_time < 6:
                final = p.ask({"Stats": True})
                if final.get("admission", final)["pending"] == 0 and s.active == 0:
                    break
                time.sleep(.03)
            elapsed = time.monotonic() - start_time
            record(mode + "-detached-work-drains-at-core-budget", final.get("admission", final)["pending"] == 0
                   and s.active == 0 and elapsed < 6, elapsedSeconds=elapsed, peerAttempts=s.attempts, state=final)
            p.close()
            if mode == "B2":
                s = peer(stall=True)
                p = start(mode, s)
                rows = p.request(RawDial=True, Address="business.invalid:80", TimeoutMS=100)
                deadline = time.monotonic() + 1
                while time.monotonic() < deadline:
                    state = p.ask({"Stats": True})
                    if state["admission"]["pending"] == 0 and s.active == 0:
                        break
                    time.sleep(.01)
                record("B2-raw-context-cancels-actual-handshake", len(rows) == 1 and not rows[0]["ok"]
                       and rows[0]["elapsedMS"] < 1000 and state["admission"]["pending"] == 0 and s.active == 0,
                       requests=rows, state=state)
                p.close()
    except BaseException as exc:
        out["fatal"] = type(exc).__name__
        print("RUNNER", type(exc).__name__, flush=True)
    finally:
        for p in probes:
            p.close()
        for s in servers:
            s.shutdown()
            s.server_close()
        out["cleanup"] = {"allOwnedProcessesExited": all(p.poll() is not None for p in processes),
                          "hostsUnchanged": before == m.sha(hosts)}
        (root / "results.json").write_text(json.dumps(out, indent=2) + "\n", encoding="utf-8")
        print("RESULT", root.relative_to(r.ROOT).as_posix(), flush=True)
    return int(bool(out.get("fatal")) or not out["checks"] or not all(c["passed"] for c in out["checks"]))


if __name__ == "__main__":
    sys.exit(main())
