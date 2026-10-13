"""Analyze a complete three-round CF IP/concurrency sweep. No trial exclusion."""
import argparse
import collections
import json
import statistics as stats
from pathlib import Path


def distribution(values):
    return {"values": values, "median": stats.median(values), "min": min(values), "max": max(values)}


def summarize(data):
    runs = data["runs"]
    warm = [r for r in runs if r["round"] == 0]
    assert len(warm) == len(data["ips"])
    reference = warm[0]["requests"][0]["hashes"]
    assert all(r["requests"][0]["hashes"] == reference for r in warm)
    lookup = {}
    for r in runs:
        assert r["ok"] and not r.get("error"), (r["round"], r["arm"], r.get("error"))
        assert r["bytes"] == data["intervalBytes"]
        assert r["peakRequests"] == r["concurrency"]
        assert r["meanActiveRequests"] <= r["concurrency"] + 0.001
        assert abs(r["mibPerSec"] - r["bytes"] / 2**20 / (r["elapsedMS"] / 1000)) < 1e-8
        hashes = []
        end = 0
        for o in sorted(r["requests"], key=lambda x: x["offset"]):
            assert not o.get("error") and o["status"] == 206
            assert o["targetIP"] in r["ips"] and o["remote"].rsplit(":", 1)[0] == o["targetIP"]
            assert o["offset"] == end and o["length"] == o["bytes"]
            end += o["length"]
            hashes.extend(o["hashes"])
        assert end == data["intervalBytes"] and hashes == reference
        if r["round"]:
            key = (r["round"], r["arm"])
            assert key not in lookup
            lookup[key] = r
    expected = {(i + 1, arm["name"]) for i, row in enumerate(data["orders"]) for arm in row}
    assert set(lookup) == expected and len(data["orders"]) == 3
    for i, row in enumerate(data["orders"]):
        actual = [r["arm"] for r in runs if r["round"] == i + 1]
        assert actual == [a["name"] for a in row]
        for arm in row:
            r = lookup[i + 1, arm["name"]]
            assert r["ips"] == arm["ips"] and r["concurrency"] == max(1, arm["concurrency"])
    names = sorted({arm for _, arm in lookup})
    summary = {"ips": data["ips"], "rounds": 3, "intervalBytes": data["intervalBytes"], "arms": {}, "pairedRatios": {}}
    for name in names:
        samples = [lookup[r, name] for r in (1, 2, 3)]
        summary["arms"][name] = {
            "mibPerSec": distribution([r["mibPerSec"] for r in samples]),
            "seconds": distribution([r["elapsedMS"] / 1000 for r in samples]),
            "meanActive": distribution([r["meanActiveRequests"] for r in samples]),
        }
    routes = [f"ip{i+1}" for i in range(len(data["ips"]))]
    if len(routes) > 1:
        routes.append("multi")
    def pair(name, top, bottom):
        summary["pairedRatios"][name] = distribution([top(r) / bottom(r) for r in (1, 2, 3)])
    def speed(r, name):
        return lookup[r, name]["mibPerSec"]
    for route in routes:
        for low, high in ((4, 6), (6, 8), (8, 12), (4, 12)):
            pair(f"{route}:{high}/{low}", lambda r: speed(r, f"{route}-{high}"), lambda r: speed(r, f"{route}-{low}"))
    if "multi" in routes:
        for n in (4, 6, 8, 12):
            for route in routes[:-1]:
                pair(f"multi/{route}:{n}", lambda r: speed(r, f"multi-{n}"), lambda r: speed(r, f"{route}-{n}"))
            pair(f"multi/fastest-observed-single:{n}", lambda r: speed(r, f"multi-{n}"), lambda r: max(speed(r, f"{route}-{n}") for route in routes[:-1]))
    requests = [o for r in runs for o in r["requests"]]
    summary["validation"] = {
        "successfulRunsIncludingWarmups": len(runs), "requests": len(requests),
        "bodyBytes": sum(r["bytes"] for r in runs), "excludedTrials": 0,
        "status": dict(collections.Counter(o["status"] for o in requests)),
        "protocol": dict(collections.Counter(o["protocol"] for o in requests)),
        "colo": dict(collections.Counter(o.get("colo", "") for o in requests)),
        "cache": dict(collections.Counter(o["cache"] for o in requests)),
        "server": dict(collections.Counter(o.get("server", "") for o in requests)),
    }
    # Endpoint-specific occupancy reconstructed from request intervals. This
    # counts requests, including connection/header wait, rather than TCP sockets.
    summary["ipOccupancy"] = []
    for r in runs:
        by_ip = {}
        for ip in r["ips"]:
            observations = [o for o in r["requests"] if o["targetIP"] == ip]
            events = sorted([(o["startMS"], 1) for o in observations] + [(o["startMS"] + o["elapsedMS"], -1) for o in observations])
            active = peak = 0
            for _, delta in events:
                active += delta
                peak = max(peak, active)
            by_ip[ip] = {"peak": peak, "mean": sum(o["elapsedMS"] for o in observations) / r["elapsedMS"], "bytes": sum(o["bytes"] for o in observations), "requests": len(observations)}
        summary["ipOccupancy"].append({"round": r["round"], "arm": r["arm"], "ips": by_ip})
    return summary


def chart(summary, path):
    import matplotlib.pyplot as plt
    fig, ax = plt.subplots(figsize=(10, 6), layout="constrained")
    routes = [f"ip{i+1}" for i in range(len(summary["ips"]))] + (["multi"] if len(summary["ips"]) > 1 else [])
    for route in routes:
        rows = [summary["arms"][f"{route}-{n}"]["mibPerSec"] for n in (4, 6, 8, 12)]
        label = "All 3 IPs, same total concurrency" if route == "multi" else f'{route}: {summary["ips"][int(route[-1])-1]}'
        ax.errorbar([4, 6, 8, 12], [r["median"] for r in rows], yerr=[[r["median"]-r["min"] for r in rows], [r["max"]-r["median"] for r in rows]], marker="o", capsize=3, label=label)
    ax.axhline(summary["arms"]["long-ip1"]["mibPerSec"]["median"], color="gray", linestyle="--", label="IP1: one 30 MiB range (median)")
    ax.set(title="CF range throughput: concurrency and destination IP", xlabel="Total concurrent 1 MiB requests", ylabel="Validated throughput (MiB/s)", xticks=[4, 6, 8, 12])
    ax.set_ylim(bottom=0)
    ax.grid(alpha=.2)
    ax.legend(fontsize=9)
    fig.text(.5, -.01, "3 complete rounds; median and observed min/max. One resource/path/period; no trials excluded.", ha="center", fontsize=9)
    fig.savefig(path, dpi=140, bbox_inches="tight")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--chart", type=Path)
    args = parser.parse_args()
    result = summarize(json.loads(args.input.read_text(encoding="utf-8")))
    args.out.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8", newline="\n")
    if args.chart:
        chart(result, args.chart)
    print(json.dumps({"arms": result["arms"], "pairedRatios": result["pairedRatios"], "validation": result["validation"]}, indent=2))
