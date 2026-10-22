"""Validate and summarize actual prefetch sessions; preserve failures and missing tasks."""
import argparse
from collections import Counter
import hashlib
import json
from pathlib import Path
import re
import statistics

from evidence_time import relative_times

ARMS = ("ordinary", "monitor", "history-two")
SONGS = (1344, 10552, 10553)


def median(values):
    return statistics.median(values) if values else None


def validate(d):
    if d["Schema"] != 1 or d["Method"] not in ("actual-prefetch-cf-history-two-v1", "actual-prefetch-cf-history-two-v2"):
        raise ValueError("unsupported product protocol")
    sessions = d["Sessions"] or []
    planned = [(r, ARMS[(j+r-1) % 3]) for r in range(1, 4) for j in range(3)]
    assert [(s["Round"], s["Arm"]) for s in sessions] == planned[:len(sessions)]
    observed = 0
    for s in sessions:
        tasks, requests, probes = (s[k] or [] for k in ("Tasks", "Requests", "Probes"))
        observed += sum(q["Bytes"] for q in requests) + sum(p["Bytes"] for p in probes)
        order = SONGS[s["Round"]-1:] + SONGS[:s["Round"]-1]
        assert [t["Song"] for t in tasks] == list(order[:len(tasks)])
        assert s["SetupBytes"] == sum(q["Bytes"] for q in requests if q["Stage"] == "setup") + sum(p["Bytes"] for p in probes)
        for q in requests:
            assert q["Bytes"] >= 0
            if q["Status"]:
                if not q["TLSVerified"] or q["Pinned"] and q["Remote"] != q["Pinned"] or d["Method"].endswith("-v2") and not q["PeerVerified"]:
                    assert q["Error"] and d["Outcome"] != "completed", "unrecorded identity failure"
        previous = set()
        for t in tasks:
            current = {f["MD5"] for f in t["Files"] or []}
            if t["OK"]:
                assert not t["Error"] and t["Source"] == "MISS"
                assert all(f["Valid"] for f in t["Files"])
                added = current - previous
                assert len(added) == 1, "missing published file"
                size = next(f["Size"] for f in t["Files"] if f["MD5"] in added)
                rows = [q for q in requests if q["Stage"] == f'task-{t["Song"]}' and q["Host"] == "play.udon.dance" and q["Status"] == 206 and not q["Error"]]
                covered = {}
                for q in rows:
                    assert q["TLSVerified"] and (not q["Pinned"] or q["Remote"] == q["Pinned"])
                    if d["Method"].endswith("-v2"):
                        assert q["PeerVerified"]
                    m = re.fullmatch(r"bytes (\d+)-(\d+)/(\d+)", q["ContentRange"])
                    assert m, "missing range"
                    start, end, full = map(int, m.groups())
                    assert full == size and q["Bytes"] == q["Length"] == end-start+1
                    assert start % (1 << 20) == 0 and end == min(start+(1 << 20), size)-1
                    if start in covered:
                        assert covered[start] == q["Hash"], "retry changed content"
                    covered[start] = q["Hash"]
                assert set(covered) == set(range(0, size, 1 << 20)), "incomplete recorded publication"
            previous = current
    assert observed == d["BodyBytes"], "body accounting mismatch"
    assert observed <= (3 << 30) or d["Outcome"] != "completed"


def summarize(path):
    raw = path.read_bytes()
    d = json.loads(raw)
    validate(d)
    verified_trace = d["Method"].endswith("-v2")
    diagnostic = d["Outcome"] == "diagnostic-completed"
    sessions = []
    for s in d["Sessions"] or []:
        requests = s["Requests"] or []
        cumulative = s["SetupMS"]
        prefix_ok = not s["SetupError"]
        tasks = []
        for t in s["Tasks"] or []:
            rows = [q for q in requests if q["Stage"] == f'task-{t["Song"]}']
            media = [q for q in rows if q["Host"] == "play.udon.dance"]
            attributed = [q for q in media if q["Status"] and (verified_trace and q.get("PeerVerified") or q["Pinned"])]
            unverified = [q for q in media if q["Status"] and not (verified_trace and q.get("PeerVerified") or q["Pinned"])]
            cumulative += t["ElapsedMS"]
            prefix_ok = prefix_ok and t["OK"]
            tasks.append({"song": t["Song"], "ok": t["OK"], "error": t["Error"], "source": t["Source"],
                          "prefixOK": prefix_ok,
                          "elapsedMS": t["ElapsedMS"], "observedSessionWallMS": t["SessionElapsedMS"],
                          "sumSetupAndTaskMS": cumulative, "selections": t["Selections"] or [],
                          "requestCount": len(rows), "mediaBodyBytes": sum(q["Bytes"] for q in media),
                          "requestErrors": dict(Counter(q["Error"] for q in rows if q["Error"])),
                          "statusCounts": dict(Counter(q["Status"] for q in rows)),
                          "mediaRemotes": dict(Counter(q["Remote"] for q in attributed)),
                          "unverifiedTracePeers": dict(Counter(q["Remote"] for q in unverified)),
                          "ignoredTraceConnections": sum(q.get("IgnoredTraceConnections", 0) for q in media),
                          "mediaCache": dict(Counter(q["Cache"] for q in media if q["Status"])),
                          "reusedAttributedMediaConnections": sum(q["Reused"] for q in attributed),
                          "allPublishedFilesValid": all(f["Valid"] for f in t["Files"] or []),
                          "publishedFileCount": len(t["Files"] or [])})
        sessions.append({"round": s["Round"], "arm": s["Arm"], "error": s["Error"], "setupError": s["SetupError"],
                         "started": s["Started"], "finished": s["Finished"], "setupMS": s["SetupMS"],
                         "setupBytes": s["SetupBytes"], "selected": s["Selected"], "tasks": tasks,
                         "missingTasks": 3-len(tasks), "probes": [{k: v for k, v in p.items() if k != "ErrorDetail"} for p in s["Probes"] or []],
                         "setupStatusCounts": dict(Counter(q["Status"] for q in requests if q["Stage"] == "setup")),
                         "setupRequestErrors": dict(Counter(q["Error"] for q in requests if q["Stage"] == "setup" and q["Error"]))})
    comparisons = {}
    by_session = {(s["round"], s["arm"]): s for s in sessions}
    for arm in (() if diagnostic else ARMS[1:]):
        pairs = []
        for round in range(1, 4):
            a = by_session.get((round, arm), {})
            b = by_session.get((round, "ordinary"), {})
            aa = {t["song"]: t for t in a.get("tasks", [])}
            bb = {t["song"]: t for t in b.get("tasks", [])}
            for song in SONGS:
                x, y = aa.get(song), bb.get(song)
                clean = bool(x and y and x["ok"] and y["ok"])
                pair = {"round": round, "song": song, "clean": clean,
                        "candidateError": x["error"] if x else "not_executed", "baselineError": y["error"] if y else "not_executed"}
                if clean:
                    pair.update(taskTimeSavedMS=y["elapsedMS"]-x["elapsedMS"], taskSpeedGain=y["elapsedMS"]/x["elapsedMS"]-1,
                                cumulativeComparable=x["prefixOK"] and y["prefixOK"])
                    if pair["cumulativeComparable"]:
                        pair.update(sumOperationTimeSavedMS=y["sumSetupAndTaskMS"]-x["sumSetupAndTaskMS"],
                                    observedSessionWallSavedMS=y["observedSessionWallMS"]-x["observedSessionWallMS"])
                pairs.append(pair)
        clean = [p for p in pairs if p["clean"]]
        comparisons[arm] = {"pairs": pairs, "cleanPairs": len(clean), "taskWins": sum(p["taskTimeSavedMS"] > 0 for p in clean),
                            "medianTaskSpeedGain": median([p["taskSpeedGain"] for p in clean]),
                            "medianTaskTimeSavedMS": median([p["taskTimeSavedMS"] for p in clean])}
    tasks = [t for s in sessions for t in s["tasks"]]
    return {"schema": 1, "method": d["Method"], "sourceSHA256": hashlib.sha256(raw).hexdigest(),
            "historySHA256": d["HistorySHA256"], "started": d["Started"], "finished": d["Finished"],
            "outcome": d["Outcome"], "error": d["Error"], "observedBodyBytes": d["BodyBytes"],
            "plannedSessions": 1 if diagnostic else 9, "executedSessions": len(sessions), "plannedTasks": 3 if diagnostic else 27,
            "executedTasks": len(tasks), "successfulTasks": sum(t["ok"] for t in tasks), "missingTasks": (3 if diagnostic else 27)-len(tasks),
            "remoteAttribution": "hostname-filtered verified TLS GotConn" if verified_trace else "v1 unpinned peers are ambiguous nested-DNS traces; retained separately, never treated as proven media peers",
            "timingScope": "task includes actual API, transfer, product MD5 validation and publication; session wall also includes harness snapshots and post-task file audits; operation sums exclude those audits and engine initialization",
            "sessions": sessions, "comparisons": comparisons}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    result = relative_times(summarize(args.source))
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=2)+"\n", encoding="utf-8", newline="\n")
    print(json.dumps({k: result[k] for k in ("outcome", "executedTasks", "successfulTasks", "missingTasks", "observedBodyBytes", "comparisons")}, indent=2))
