"""Evaluate predeclared calibration-01 predictors without choosing new history."""
import argparse
import json
from pathlib import Path

from analyze_selection import summarize

HISTORY_HASH = "fb8a2221d52ae2e0f5318d15075dd94663d12da097e24f5303983fad8d3cb640"
PREDICTORS = {
    "prior-dns-short": "104.26.5.28",
    "prior-dns-full": "2606:4700:20::681a:41c",
    "prior-extra-full": "104.16.0.1",
}


def compare(previous, current):
    old, new = summarize(previous), summarize(current)
    if old["sourceSHA256"] != HISTORY_HASH:
        raise ValueError("history was not the predeclared frozen window")
    costs = new["referenceMeanMS"]
    best = min(costs.values()) if costs else None
    rows = []
    for name, ip in PREDICTORS.items():
        cost = costs.get(ip)
        rows.append({"predictor": name, "ip": ip, "priorMeanMS": old["referenceMeanMS"].get(ip),
                     "currentMeanMS": cost, "measuredCleanTwice": cost is not None,
                     "fractionOfCurrentReferenceBest": best / cost if cost is not None else None,
                     "currentRank": 1 + sum(v < cost for v in costs.values()) if cost is not None else None})
    common = sorted(set(costs) & set(old["referenceMeanMS"]))
    concordant = discordant = tied = 0
    for i, a in enumerate(common):
        for b in common[i+1:]:
            product = (costs[a]-costs[b]) * (old["referenceMeanMS"][a]-old["referenceMeanMS"][b])
            concordant += product > 0
            discordant += product < 0
            tied += product == 0
    return {"schema": 1, "previousSHA256": old["sourceSHA256"], "currentSHA256": new["sourceSHA256"],
            "scope": "same song 1343, later window, same Wi-Fi; no cross-network or cross-day claim",
            "previousOutcome": old["outcome"], "currentOutcome": new["outcome"],
            "previousSelected": old["selected"], "currentSelected": new["selected"],
            "currentReferenceBestMS": best, "currentReferenceCandidates": len(costs), "predictors": rows,
            "commonCandidates": common, "concordantRankPairs": concordant, "discordantRankPairs": discordant,
            "tiedRankPairs": tied, "newlyQualified": sorted(set(costs)-set(old["referenceMeanMS"])),
            "noLongerQualified": sorted(set(old["referenceMeanMS"])-set(costs))}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("previous", type=Path)
    parser.add_argument("current", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    result = compare(args.previous, args.current)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=2)+"\n", encoding="utf-8", newline="\n")
    print(json.dumps(result, indent=2))
