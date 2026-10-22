"""Session cost and ordinary-path audit of the frozen PRODUCT protocol.

No new samples or rankings: pair whole sessions by round, and only attribute
unpinned peers from the corrected v2 recorder. Request intervals can overlap.
"""
import argparse
from collections import Counter
from datetime import datetime
import hashlib
import ipaddress
import json
from pathlib import Path
import statistics

from analyze_product import ARMS, validate


def elapsed_ms(start, end):
    # RFC3339 timestamps have sub-microsecond precision; this report rounds to ms.
    return round((datetime.fromisoformat(end) - datetime.fromisoformat(start)).total_seconds() * 1000, 3)


def distribution(values):
    return {"n": len(values), "min": min(values), "median": statistics.median(values), "max": max(values)} if values else {"n": 0}


def session_cost(s):
    tasks = s["Tasks"] or []
    complete = not (s["Error"] or s["SetupError"]) and len(tasks) == 3 and all(t["OK"] for t in tasks)
    return {
        "round": s["Round"], "arm": s["Arm"], "complete": complete,
        "error": s["Error"], "setupError": s["SetupError"],
        "executedTasks": len(tasks), "successfulTasks": sum(t["OK"] for t in tasks),
        "setupMS": s["SetupMS"], "setupBytes": s["SetupBytes"],
        "bodyBytes": sum(q["Bytes"] for q in s["Requests"] or []) + sum(p["Bytes"] for p in s["Probes"] or []),
        "fullSessionWallMS": elapsed_ms(s["Started"], s["Finished"]),
        "businessCompletionWallMS": tasks[-1]["SessionElapsedMS"] if complete else None,
        "sumSetupAndTaskMS": s["SetupMS"] + sum(t["ElapsedMS"] for t in tasks) if complete else None,
    }


def pair_sessions(sessions):
    by_key = {(s["Round"], s["Arm"]): s for s in sessions}
    pairs = []
    for rnd in range(1, 4):
        baseline = by_key.get((rnd, "ordinary"))
        for arm in ARMS[1:]:
            candidate = by_key.get((rnd, arm))
            pair = {"round": rnd, "arm": arm, "comparable": False,
                    "baselineState": "not_executed" if baseline is None else session_cost(baseline),
                    "candidateState": "not_executed" if candidate is None else session_cost(candidate)}
            if baseline and candidate:
                b, c = session_cost(baseline), session_cost(candidate)
                pair["comparable"] = b["complete"] and c["complete"]
                if pair["comparable"]:
                    assert [t["Song"] for t in baseline["Tasks"]] == [t["Song"] for t in candidate["Tasks"]]
                    pair.update(
                        fullSessionSavedMS=b["fullSessionWallMS"]-c["fullSessionWallMS"],
                        businessCompletionSavedMS=b["businessCompletionWallMS"]-c["businessCompletionWallMS"],
                        sumSetupAndTaskSavedMS=b["sumSetupAndTaskMS"]-c["sumSetupAndTaskMS"],
                        extraBodyBytes=c["bodyBytes"]-b["bodyBytes"],
                        prefixes=[{"tasks": i+1, "throughSong": bt["Song"],
                                   "observedWallSavedMS": bt["SessionElapsedMS"]-ct["SessionElapsedMS"]}
                                  for i, (bt, ct) in enumerate(zip(baseline["Tasks"], candidate["Tasks"]))])
            pairs.append(pair)
    return pairs


def ordinary_paths(d):
    verified = d["Method"].endswith("-v2")
    result = []
    for s in d["Sessions"] or []:
        if s["Arm"] != "ordinary":
            continue
        for task in s["Tasks"] or []:
            rows = [q for q in s["Requests"] or [] if q["Stage"] == f'task-{task["Song"]}']
            media = [q for q in rows if q["Host"] == "play.udon.dance"]
            api = [q for q in rows if q["Host"] == "api.udon.dance"]
            accepted = [q for q in media if verified and q.get("PeerVerified") and q["TLSVerified"] and q["Status"]]
            peers = sorted({q["Remote"] for q in accepted})
            origin = elapsed_ms(s["Started"], task["Started"])

            def envelope(requests):
                if not requests:
                    return None
                start, end = min(q["StartMS"] for q in requests), max(q["EndMS"] for q in requests)
                return {"startAfterTaskMS": start-origin, "endAfterTaskMS": end-origin, "wallSpanMS": end-start}

            result.append({
                "round": s["Round"], "song": task["Song"], "ok": task["OK"],
                "taskElapsedMS": task["ElapsedMS"], "requestCount": len(rows),
                "requestErrors": dict(Counter(q["Error"] for q in rows if q["Error"])),
                "statusCounts": dict(Counter(str(q["Status"]) for q in rows)),
                "mediaCache": dict(Counter(q["Cache"] for q in media if q["Status"])),
                "unattributedMediaResponses": sum(bool(q["Status"]) for q in media)-len(accepted),
                "peers": [{"ip": ip, "family": ipaddress.ip_address(ip).version,
                           "responses": sum(q["Remote"] == ip for q in accepted),
                           "bodyBytes": sum(q["Bytes"] for q in accepted if q["Remote"] == ip),
                           "reusedResponses": sum(q["Reused"] for q in accepted if q["Remote"] == ip)} for ip in peers],
                "apiEnvelope": envelope(api), "mediaEnvelope": envelope(media),
                "mediaResponseHeadersWaitMS": distribution([q["HeadersMS"]-q["StartMS"] for q in media]),
                "mediaHeadersToBodyCloseMS": distribution([q["EndMS"]-q["HeadersMS"] for q in media if q["Status"]]),
                "afterLastRecordedRequestMS": task["ElapsedMS"]-(max(q["EndMS"] for q in rows)-origin) if rows else None,
            })
    return result


def audit(path):
    raw = path.read_bytes()
    d = json.loads(raw)
    validate(d)
    assert d["Outcome"] != "running", "only final windows can be audited"
    diagnostic = d["Outcome"] == "diagnostic-completed"
    return {"schema": 1, "sourceSHA256": hashlib.sha256(raw).hexdigest(), "method": d["Method"],
            "outcome": d["Outcome"], "bodyBytes": d["BodyBytes"],
            "sessions": [session_cost(s) for s in d["Sessions"] or []],
            "pairs": [] if diagnostic else pair_sessions(d["Sessions"] or []),
            "ordinaryPaths": ordinary_paths(d),
            "scope": {
                "gain": "ordinary minus strategy, paired by round and identical ordered songs; positive means saved time",
                "fullSession": "Started to Finished, includes setup, actual business, harness saves/file audits and deferred cleanup",
                "businessCompletion": "session start to last prefetch return; includes preceding harness work but excludes last external file audit and cleanup",
                "sum": "sensitivity only: setup plus sequential task stopwatches; never used as wall time for concurrent work",
                "requests": "interval envelopes overlap; headers wait includes pool/DNS/TCP/TLS/server time, not first-byte or isolated connect time; body interval includes consumer and Close time",
                "residual": "after final recorded request includes unseparated local work; not a pure MD5, disk or scheduler measurement",
                "history": "v1 unpinned peers are not attributed; v2 uses hostname-verified TLS; unused candidates have no inferred quality",
                "experience": "prefetch only; no Range delivered to a player, first frame, playback stall or concurrent Monitor result"}}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    report = audit(args.source)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=2)+"\n", encoding="utf-8", newline="\n")
    print(json.dumps({"outcome": report["outcome"], "sessions": len(report["sessions"]),
                      "pairs": [{k: p[k] for k in ("round", "arm", "comparable", "fullSessionSavedMS", "businessCompletionSavedMS") if k in p} for p in report["pairs"]]}, indent=2))
