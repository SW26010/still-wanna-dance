"""Recompute paired comparisons; never drop failed or incomplete windows."""
import argparse
import hashlib
import json
import statistics
from pathlib import Path


def median(values):
    return statistics.median(values) if values else None


def comparison(base, extra):
    return {
        "throughputGainPercent": (base["ElapsedMS"] / extra["ElapsedMS"] - 1) * 100,
        "timeSavedPercent": (1 - extra["ElapsedMS"] / base["ElapsedMS"]) * 100,
        "prefix8ChangePercent": (extra["Prefix8MS"] / base["Prefix8MS"] - 1) * 100,
    }


def summarize(path, reference_arm="dns", pair_arms=("dns", "extra")):
    raw = path.read_bytes()
    w = json.loads(raw)
    runs = w.get("Runs") or []
    plan = w["Plan"]
    assert len(runs) <= len(plan), "extra unplanned runs"
    refs = {}
    groups = {}
    brief = []
    for i, r in enumerate(runs):
        assert all(r[k] == plan[i][k] for k in ("Round", "Song", "Arm")), "execution order"
        assert r["IP"] == w["Selected"][r["Arm"]], "selection changed after training"
        requests = r.get("Requests") or []
        assert sum(q["Bytes"] for q in requests) == r["Bytes"], "byte accounting"
        good = [q for q in requests if not q["Error"]]
        for q in good:
            assert q["Status"] == 206 and q["Remote"] == r["IP"]
            assert q["Bytes"] == q["Length"]
            assert q["Length"] == min(1 << 20, r["Size"] - q["Offset"])
        if r["OK"]:
            assert len({q["Offset"] for q in good}) == (r["Size"] + (1 << 20) - 1) // (1 << 20)
            assert len(good) == len(requests), "successful run has failed requests"
            assert r["ElapsedMS"] > 0 and r["Prefix8MS"] > 0
            if r["Round"] == 0 and r["Arm"] == reference_arm:
                refs[r["Song"]] = {q["Offset"]: q["Hash"] for q in good}
            else:
                assert all(q["Hash"] == refs[r["Song"]][q["Offset"]] for q in good), "content mismatch"
        if r["Round"]:
            groups.setdefault((r["Song"], r["Round"]), {})[r["Arm"]] = r
        latencies = sorted(q["EndMS"] - q["StartMS"] for q in good)
        p95 = latencies[min(len(latencies)-1, int(len(latencies)*.95))] if latencies else None
        brief.append({
            "round": r["Round"], "song": r["Song"], "arm": r["Arm"], "ip": r["IP"],
            "started": r["Started"], "ok": r["OK"], "error": r["Error"],
            "size": r["Size"], "bytes": r["Bytes"], "elapsedMS": r["ElapsedMS"],
            "mibPerSecond": r["Size"] / (1 << 20) / (r["ElapsedMS"] / 1000) if r["OK"] else None,
            "cpuMSPerMiB": r["CPUMS"] / (r["Size"] / (1 << 20)),
            "firstBlockMS": r["FirstBlockMS"], "prefix8MS": r["Prefix8MS"],
            "blockP95MS": p95, "peakActive": r["PeakActive"], "peakOpen": r["PeakOpen"],
            "requests": len(requests), "failedRequests": len(requests)-len(good),
            "colos": sorted({q["Colo"] for q in requests}),
            "cacheStates": sorted({q["Cache"] for q in requests}),
            "protocols": sorted({q["Protocol"] for q in requests}),
            "loadCompletedAfterRetry": r["Error"] == "request_failed",
        })
    pairs = []
    failed_pairs = []
    for key, arms in sorted(groups.items()):
        arms = {a: r for a, r in arms.items() if a in pair_arms}
        if set(arms) != set(pair_arms) or not all(r["OK"] for r in arms.values()):
            failed_pairs.append({"song": key[0], "round": key[1], "arms": {a: r["Error"] for a, r in arms.items()}})
            continue
        pairs.append({"song": key[0], "round": key[1], **comparison(arms[pair_arms[0]], arms[pair_arms[1]])})
    training = []
    for source in sorted(path.parent.glob("monitor-*-private.json")):
        data = source.read_bytes()
        snapshot = json.loads(data)
        training.append({
            "name": source.name.removesuffix("-private.json"),
            "sourceSHA256": hashlib.sha256(data).hexdigest(),
            "results": [{"ip": r.get("ip"), "state": r["state"],
                         "speedBPS": r.get("estimatedSpeedBPS"),
                         "observedAt": r.get("throughputObservedAt")}
                        for r in snapshot["results"] if r["operation"] == "resource"],
            "playbackURLResults": [{"ip": r.get("ip"), "state": r["state"], "reason": r.get("reason")}
                                   for r in snapshot["results"] if r["operation"] == "playback_url"],
        })
    return {
        "window": path.parent.name, "sourceSHA256": hashlib.sha256(raw).hexdigest(),
        "started": w["Started"], "finished": w["Finished"], "outcome": w["Outcome"],
        "error": w["Error"], "dns": [a["IP"] for a in w.get("DNS") or []], "selected": w["Selected"],
        "plannedRuns": len(plan), "completedRuns": len(runs), "missingRuns": len(plan)-len(runs),
        "bodyBytes": sum(r["Bytes"] for r in runs),
        "failedRuns": sum(not r["OK"] for r in runs), "failedPairs": failed_pairs,
        "medianGainPercent": median([p["throughputGainPercent"] for p in pairs]),
        "winCount": sum(p["throughputGainPercent"] > 0 for p in pairs),
        "pairs": pairs, "runs": brief, "training": training,
    }


def aggregate(windows):
    pairs = [p for w in windows for p in w["pairs"]]
    gains = [p["throughputGainPercent"] for p in pairs]
    per_song = {}
    for song in sorted({p["song"] for p in pairs}):
        selected = [p for p in pairs if p["song"] == song]
        per_song[str(song)] = {"pairs": len(selected), "medianGainPercent": median([p["throughputGainPercent"] for p in selected])}
    # Repeats are nested in resources and time windows, not independent blocks.
    strata = []
    for w in windows:
        for song in sorted({p["song"] for p in w["pairs"]}):
            strata.append({"window": w["window"], "song": song, "medianGainPercent": median([p["throughputGainPercent"] for p in w["pairs"] if p["song"] == song])})
    return {
        "schema": 1, "windows": windows, "completeWindowCount": sum(w["outcome"] == "complete" for w in windows),
        "successfulPairs": len(pairs), "medianGainPercent": median(gains),
        "winFraction": sum(g > 0 for g in gains) / len(gains) if gains else None,
        "medianPrefix8ChangePercent": median([p["prefix8ChangePercent"] for p in pairs]),
        "bySong": per_song, "resourceWindowStrata": strata,
        "decision": "pending_cross_time_evidence" if sum(w["outcome"] == "complete" for w in windows) < 3 else "evaluate_preregistered_criteria_and_limit_scope",
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("windows", nargs="+", type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    result = aggregate([summarize(p) for p in args.windows])
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2)+"\n", encoding="utf-8")
    print(json.dumps({k: v for k, v in result.items() if k not in ("windows", "resourceWindowStrata")}, ensure_ascii=False, indent=2))
