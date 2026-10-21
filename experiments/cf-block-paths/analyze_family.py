"""Four-arm held-out comparison; old two-arm windows are never pooled here."""
import argparse
import hashlib
import json
import statistics
from pathlib import Path
from analyze import comparison, summarize, median
from evidence_time import relative_times

COMPARISONS = [("auto", "extra"), ("v4", "extra"), ("v6", "extra"),
               ("v4", "v6"), ("auto", "v4"), ("auto", "v6")]


def pairs_for(runs, base, challenger):
    groups = {}
    for r in runs:
        if r["round"] and r["arm"] in (base, challenger):
            groups.setdefault((r["song"], r["round"]), {})[r["arm"]] = r
    pairs, incomplete = [], []
    for (song, round_), arms in sorted(groups.items()):
        if len(arms) != 2 or not all(r["ok"] for r in arms.values()):
            incomplete.append({"song": song, "round": round_, "arms": {a: r["error"] for a, r in arms.items()}})
            continue
        a, b = arms[base], arms[challenger]
        p = comparison({"ElapsedMS": a["elapsedMS"], "Prefix8MS": a["prefix8MS"]},
                       {"ElapsedMS": b["elapsedMS"], "Prefix8MS": b["prefix8MS"]})
        pairs.append({"song": song, "round": round_, "sameIP": a["ip"] == b["ip"], **p})
    return pairs, incomplete


def family_window(path):
    raw = json.loads(path.read_bytes())
    assert raw["Schema"] in (2, 3), "Do not mix original two-arm data with family data"
    w = summarize(path, reference_arm="auto", pair_arms=("auto", "extra"))
    w["method"] = "legacy-family-v2" if raw["Schema"] == 2 else raw["Method"]
    source = path.parent / "training.json"
    training_bytes = source.read_bytes()
    training = json.loads(training_bytes)
    references = {}
    training_runs = []
    for r in training.get("Runs") or []:
        qs = r.get("Requests") or []
        assert sum(q["Bytes"] for q in qs) == r["Bytes"]
        if r["OK"]:
            assert all(not q["Error"] and q["Status"] == 206 and q["Remote"] == r["IP"] and q["Bytes"] == q["Length"] for q in qs)
            assert len({q["Offset"] for q in qs}) == (r["Size"] + (1 << 20) - 1) // (1 << 20)
            hashes = {q["Offset"]: q["Hash"] for q in qs}
            if not references:
                references = hashes
            assert hashes == references, "training content changed"
        training_runs.append({k: r[k] for k in ("Round", "Song", "Arm", "IP", "Size", "Bytes", "OK", "Error", "ElapsedMS", "PeakActive")})
    candidates = training.get("Candidates") or []
    for c in candidates:
        matching = [r for r in training_runs if r["IP"] == c["IP"]]
        observed = [r["ElapsedMS"] for r in matching if r["OK"]]
        assert observed == (c.get("DurationsMS") or [])
        assert sum(not r["OK"] for r in matching) == c["Failures"]
        # A stopped window may have persisted the third sample before final
        # selection computed MedianMS. Keep that partial evidence analyzable.
        if len(observed) == 3 and (c["MedianMS"] or training.get("Winners")):
            assert abs(statistics.median(observed) - c["MedianMS"]) < 1e-8
    for arm, chosen in (training.get("Winners") or {}).items():
        eligible = [c for c in candidates if c["Arm"] == arm and not c["Failures"] and len(c.get("DurationsMS") or []) == 3]
        assert chosen == min(eligible, key=lambda c: c["MedianMS"])["IP"], "winner changed after training"
        assert not raw["Selected"] or chosen == raw["Selected"][arm]
    w["blockTraining"] = {"sourceSHA256": hashlib.sha256(training_bytes).hexdigest(),
                          "candidates": candidates, "runs": training_runs,
                          "winners": training["Winners"],
                          "bodyBytes": sum(r["Bytes"] for r in training_runs)}
    w["comparisons"] = {}
    for base, challenger in COMPARISONS:
        pairs, failed = pairs_for(w["runs"], base, challenger)
        w["comparisons"][f"{challenger}_vs_{base}"] = {"pairs": pairs, "failedPairs": failed,
            "medianGainPercent": median([p["throughputGainPercent"] for p in pairs]),
            "wins": sum(p["throughputGainPercent"] > 0 for p in pairs),
            "sameIPPairs": sum(p["sameIP"] for p in pairs)}
    return w


def aggregate_family(windows):
    methods = {w["method"] for w in windows}
    assert len(methods) == 1, "Changed experiment methods must be analyzed separately"
    result = {"schema": 2, "completeWindows": sum(w["outcome"] == "complete" for w in windows),
              "method": next(iter(methods)), "windows": windows, "comparisons": {}}
    for base, challenger in COMPARISONS:
        key = f"{challenger}_vs_{base}"
        pairs = [p for w in windows for p in w["comparisons"][key]["pairs"]]
        gains = [p["throughputGainPercent"] for p in pairs]
        result["comparisons"][key] = {
            "successfulPairs": len(pairs), "medianGainPercent": median(gains),
            "rangeGainPercent": [min(gains), max(gains)] if gains else [],
            "winFraction": sum(g > 0 for g in gains)/len(gains) if gains else None,
            "medianPrefix8ChangePercent": median([p["prefix8ChangePercent"] for p in pairs]),
            "sameIPPairs": sum(p["sameIP"] for p in pairs),
            "bySong": {str(song): median([p["throughputGainPercent"] for p in pairs if p["song"] == song]) for song in sorted({p["song"] for p in pairs})},
            "byWindow": {w["window"]: w["comparisons"][key]["medianGainPercent"] for w in windows},
        }
    result["decision"] = "pending_cross_time_evidence" if result["completeWindows"] < 3 else "evaluate_each_comparison_and_same_ip_noise"
    return result


if __name__ == "__main__":
    p = argparse.ArgumentParser()
    p.add_argument("windows", nargs="+", type=Path)
    p.add_argument("--out", required=True, type=Path)
    args = p.parse_args()
    result = relative_times(aggregate_family([family_window(path) for path in args.windows]))
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=2, ensure_ascii=False)+"\n", encoding="utf-8", newline="\n")
    print(json.dumps({k: v for k, v in result.items() if k != "windows"}, indent=2))
