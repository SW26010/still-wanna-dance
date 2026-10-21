"""Summarize the bounded DNS observation without interpreting cancellations as outages."""
import argparse
import collections
import hashlib
import ipaddress
import json
from pathlib import Path


def summarize_dns(path):
    raw = path.read_bytes()
    data = json.loads(raw)
    observations, lookups, answers = (data[k] for k in ("Observations", "Lookups", "Answers"))
    memberships, anomalies, counts = [], [], collections.Counter()
    previous = None
    for row in observations:
        ips = sorted(a["IP"] for a in row["Addresses"] or [])
        v4 = sum(ipaddress.ip_address(ip).version == 4 for ip in ips)
        v6 = len(ips) - v4
        counts[f"v4={v4},v6={v6}"] += 1
        if ips != previous:
            memberships.append({"started": row["Started"], "ips": ips})
            previous = ips
        if row["Error"] or not v4 or not v6:
            anomalies.append(row)
    successful = [r for r in lookups if not r["Error"]]
    return {"schema": 1, "sourceSHA256": hashlib.sha256(raw).hexdigest(),
            "firstObservation": observations[0]["Started"], "lastObservation": observations[-1]["Started"],
            "observations": len(observations), "membershipCounts": dict(counts),
            "membershipChanges": memberships, "missingFamilyOrErrorObservations": anomalies,
            "lookups": lookups, "lookupCount": len(lookups),
            "zeroTTLSuccessfulLookups": sum(r["TTLMS"] == 0 for r in successful),
            "oneSecondSuccessfulLookups": sum(r["TTLMS"] == 1000 for r in successful),
            "answers": answers, "answerCount": len(answers),
            "answerOutcomes": dict(collections.Counter(r["Error"] or "success" for r in answers)),
            "interpretation": "Collector cancellation is expected; missing-family and zero-TTL fixtures are mechanism evidence, not attribution of the earlier stopped download window."}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    result = summarize_dns(args.source)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2)+"\n", encoding="utf-8")
    print(json.dumps({k: v for k, v in result.items() if k not in ("answers", "lookups")}, ensure_ascii=False, indent=2))
