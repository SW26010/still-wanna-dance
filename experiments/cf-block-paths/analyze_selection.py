"""Finite-candidate calibration; no inferred success for failed or missing work."""
import argparse
import collections
import hashlib
import json
import math
import statistics
from pathlib import Path

from evidence_time import relative_times


def median(values):
    return statistics.median(values) if values else None


def compact_run(row):
    return {k: row[k] for k in ("Song", "Arm", "IP", "Round", "OK", "Error", "ElapsedMS", "Size",
                               "Bytes", "Prefix8MS", "FirstBlockMS", "CPUMS", "PeakActive")}


def validate_measurements(d):
    references = {}
    runs = (d.get("Training") or []) + (d.get("Evaluation") or [])
    for r in runs:
        requests = r.get("Requests") or []
        assert sum(q["Bytes"] for q in requests) == r["Bytes"], "run byte accounting"
        good = [q for q in requests if not q["Error"]]
        for q in good:
            assert q["Status"] == 206 and q["Remote"] == r["IP"], "response/remote mismatch"
            assert q["Bytes"] == q["Length"] == min(1 << 20, r["Size"] - q["Offset"]), "block length"
        hashes = {q["Offset"]: q["Hash"] for q in good}
        if r["Arm"] in ("reference", "integrity") and r["OK"]:
            references[r["Song"]] = hashes
        elif r["Song"] in references:
            assert all(references[r["Song"]].get(k) == v for k,v in hashes.items()), "content mismatch"
        if r["OK"]:
            assert len(good) == len(requests), "clean run contains failed request"
            assert len(hashes) == (r["Size"] + (1 << 20) - 1) // (1 << 20), "incomplete coverage"
            assert r["Bytes"] == r["Size"], "complete size"
    assert sum(r["Bytes"] for r in runs) + sum(p["Bytes"] for p in d.get("Probes") or []) == d["BodyBytes"], "total body accounting"
    for p in d.get("Probes") or []:
        if not p["Error"]:
            assert p["Status"] == 206 and p["TLSVerified"] and p["Remote"] == p["IP"], "probe response/identity"
            assert p["Bytes"] == int(p["Kind"].removeprefix("single-")), "probe length"
            assert len(p["Hash"]) == 64, "probe content digest"
    planned = []
    for song in (1344, 10552, 10553):
        planned.append((song, 0, "integrity"))
        for round, arms in ((0,("dns-short","union-short","union-block")),
                            (1,("union-short","union-block","dns-short")),
                            (2,("dns-short","union-block","union-short"))):
            planned.extend((song,round,arm) for arm in arms)
    actual = [(r["Song"],r["Round"],r["Arm"]) for r in d.get("Evaluation") or []]
    assert actual == planned[:len(actual)], "evaluation order changed"
    for r in d.get("Evaluation") or []:
        arm = "dns-short" if r["Arm"] == "integrity" else r["Arm"]
        assert r["IP"] == d["Selected"][arm], "selection changed after training"
    if d["Outcome"] == "completed":
        assert len(actual) == len(planned), "completed window has missing evaluations"


def paired(rows, arm, baseline="dns-short"):
    selected = {(r["Song"], r["Round"]): r for r in rows if r["Arm"] == arm and r["Round"] > 0}
    base = {(r["Song"], r["Round"]): r for r in rows if r["Arm"] == baseline and r["Round"] > 0}
    result = []
    for key in sorted(selected.keys() | base.keys()):
        a, b = selected.get(key), base.get(key)
        row = {"song": key[0], "round": key[1], "completeCleanPair": bool(a and b and a["OK"] and b["OK"])}
        if row["completeCleanPair"]:
            row.update(throughputGain=b["ElapsedMS"] / a["ElapsedMS"] - 1,
                       timeSavedMS=b["ElapsedMS"] - a["ElapsedMS"],
                       prefix8Change=a["Prefix8MS"] / b["Prefix8MS"] - 1,
                       sameIP=a["IP"] == b["IP"])
        else:
            row.update(candidateError=a["Error"] if a else "not_executed",
                       baselineError=b["Error"] if b else "not_executed")
        result.append(row)
    clean = [r for r in result if r["completeCleanPair"]]
    return {"pairs": result, "cleanPairs": len(clean), "wins": sum(r["throughputGain"] > 0 for r in clean),
            "medianThroughputGain": median([r["throughputGain"] for r in clean]),
            "medianTimeSavedMS": median([r["timeSavedMS"] for r in clean]),
            "medianPrefix8Change": median([r["prefix8Change"] for r in clean])}


def cost_replay(comparison, full_cost, baseline_cost):
    clean = [p for p in comparison["pairs"] if p["completeCleanPair"]]
    changed = [p for p in clean if not p["sameIP"]]
    # Repeated labels on one IP measure noise, not a benefit of extra candidates.
    saved = median([p["timeSavedMS"] for p in changed])
    return {"kind": "offline-cost-replay-not-end-to-end-product-test",
            "fullSearchMS": full_cost, "extraSearchBeyondSameDNSMethodMS": full_cost - baseline_cost,
            "differentIPCleanPairs": len(changed), "sameIPControlPairs": len(clean) - len(changed),
            "medianHoldoutSavingPerTaskMS": saved,
            "tasksToAmortizeFullSearch": math.ceil(full_cost / saved) if saved and saved > 0 else None,
            "tasksToAmortizeExtraSearch": math.ceil((full_cost - baseline_cost) / saved) if saved and saved > 0 else None,
            "firstTaskNetChangeAgainstAlreadySelectedDNSMS": full_cost - saved if saved is not None else None,
            "firstTaskNetChangeWhenBothSearchMS": full_cost - baseline_cost - saved if saved is not None else None}


def summarize(path):
    raw = path.read_bytes()
    d = json.loads(raw)
    if d["Schema"] != 1 or d["Method"] != "equal-membership-cold-probes-v1":
        raise ValueError("unsupported selection method")
    validate_measurements(d)
    candidates = d["Candidates"] or []
    probes, training, evaluation = (d[k] or [] for k in ("Probes", "Training", "Evaluation"))
    reference = {}
    for c in candidates:
        runs = [r for r in training if r["Arm"] == "training" and r["IP"] == c["IP"]]
        if len(runs) == 2 and all(r["OK"] for r in runs):
            reference[c["IP"]] = statistics.mean(r["ElapsedMS"] for r in runs)
    oracle = min(reference.values()) if reference else None
    ranks = []
    for kind, metric in (("single-1048576", "TCPMS"), ("single-1048576", "HeadersMS"),
                         ("single-1048576", "ElapsedMS"), ("single-8388608", "ElapsedMS")):
        # TCP ranking includes known HTTP connections whose bodies later failed.
        # A failure before HTTP has no trustworthy media remote and is excluded.
        # This is a trace-derived comparison, not an execution of CFST itself.
        eligible = [p for p in probes if p["Kind"] == kind and p[metric] > 0
                    and (metric == "TCPMS" and p["Remote"] or not p["Error"])]
        eligible.sort(key=lambda p: p[metric])
        for k in (1, 2, 4):
            kept = [p["IP"] for p in eligible[:k]]
            costs = [reference[ip] for ip in kept if ip in reference]
            ratio = oracle / min(costs) if oracle and costs else None
            ranks.append({"kind": kind, "metric": metric, "keep": k, "retained": kept,
                          "fractionOfReferenceBest": ratio, "retains90Percent": ratio is not None and ratio >= .9})
    requests = [q for r in training + evaluation for q in (r.get("Requests") or [])]
    source = {c["IP"]: c["Source"] for c in candidates}
    costs = {}
    for kind in ("single-1048576", "single-8388608"):
        costs[kind] = {}
        for scope in ("dns", "historical", "sampled", "all"):
            ps = [p for p in probes if p["Kind"] == kind and (scope == "all" or source[p["IP"]] == scope)]
            costs[kind][scope] = {"attempts": len(ps), "failures": sum(bool(p["Error"]) for p in ps),
                                  "bodyBytes": sum(p["Bytes"] for p in ps), "sumElapsedMS": sum(p["ElapsedMS"] for p in ps)}
    comparisons = {arm: paired(evaluation, arm) for arm in ("union-short", "union-block")}
    # Cost replay is explicitly NOT a measured product invocation: omit research
    # pauses/integrity reference/full oracle training, count every failed probe.
    replay_cost = costs["single-8388608"]["all"]["sumElapsedMS"]
    baseline_cost = costs["single-8388608"]["dns"]["sumElapsedMS"]
    replay = cost_replay(comparisons["union-short"], replay_cost, baseline_cost)
    sanitized_probes = [{k: v for k, v in p.items() if k != "ErrorDetail"} for p in probes]
    return {"schema": 1, "method": d["Method"], "sourceSHA256": hashlib.sha256(raw).hexdigest(),
            "started": d["Started"], "finished": d["Finished"], "outcome": d["Outcome"], "error": d["Error"],
            "candidates": candidates, "unqualified": d.get("Unqualified") or [], "selected": d["Selected"],
            "observedBodyBytes": d["BodyBytes"], "preparationMS": d["PreparationMS"],
            "screenWallMS": d["ScreenMS"], "fullReferenceWallMS": d["ReferenceMS"],
            "plannedShortProbes": 2 * len(candidates), "executedShortProbes": len(probes),
            "missingShortProbes": 2 * len(candidates) - len(probes),
            "plannedEvaluationLoads": 30, "executedEvaluationLoads": len(evaluation),
            "missingEvaluationLoads": 30 - len(evaluation),
            "plannedFormalLoads": 18,
            "executedFormalLoads": sum(r["Round"] > 0 for r in evaluation),
            "probes": sanitized_probes, "referenceMeanMS": reference, "screenRetention": ranks,
            "training": [compact_run(r) for r in training], "evaluation": [compact_run(r) for r in evaluation],
            "requestCount": len(requests), "requestFailures": dict(collections.Counter(q["Error"] for q in requests if q["Error"])),
            "cacheCounts": dict(collections.Counter(q["Cache"] for q in requests)),
            "coloCounts": dict(collections.Counter(q["Colo"] for q in requests)),
            "reusedConnections": sum(q["Reused"] for q in requests),
            "probeCosts": costs, "comparisons": comparisons, "costReplay": replay}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    result = relative_times(summarize(args.source))
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")
    print(json.dumps({k: result[k] for k in ("outcome", "selected", "observedBodyBytes", "comparisons", "costReplay")}, ensure_ascii=False, indent=2))
