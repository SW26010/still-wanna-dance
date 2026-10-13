"""Summarize complete, paired CF experiment rounds; plotting is optional."""
import argparse
import json
import statistics
from pathlib import Path

ARMS = ["range-1", "range-2", "range-3", "range-4", "long"]


def summarize(paths, exclude):
    selected, excluded, warmups, signatures = [], [], [], []
    transferred = requests = 0
    protocols, cache_states, colos, servers = set(), set(), set(), set()
    for path in paths:
        session = json.loads(path.read_text(encoding="utf-8"))
        warm = session["runs"][0]
        if not warm["ok"] or warm["round"] != 0:
            raise ValueError("Missing successful warmup: " + path.name)
        signatures.append((session["host"], session["ip"], session["song"],
                           session["resourceBytes"], session["intervalBytes"],
                           warm["requests"][0]["hashes"]))
        for original in session["runs"]:
            row = dict(original, session=path.stem)
            transferred += row["bytes"]
            requests += sum(o["length"] > 0 for o in row["requests"])
            for o in row["requests"]:
                if o["length"]:
                    protocols.add(o["protocol"])
                    cache_states.add(o["cache"])
                    colos.add(o.get("colo", ""))
                    servers.add(o.get("server", ""))
            key = f"{path.stem}:{row['round']}"
            if row["round"] == 0:
                warmups.append(row)
            elif key in exclude:
                excluded.append(row)
            else:
                selected.append(row)
    if any(s != signatures[0] for s in signatures[1:]):
        raise ValueError("Sessions differ in resource, fixed route, interval or content")
    cycles = {}
    for row in selected:
        key = f"{row['session']}:{row['round']}"
        arms = cycles.setdefault(key, {})
        if row["arm"] in arms:
            raise ValueError("Duplicate arm: " + key)
        arms[row["arm"]] = row
    if not cycles or any(set(arms) != set(ARMS) for arms in cycles.values()):
        raise ValueError("Incomplete paired rounds; retain failures separately, do not silently drop arms")
    if any(not row["ok"] for row in selected):
        raise ValueError("A primary arm failed; report failure instead of computing success-only speedups")
    summaries = {}
    for arm in ARMS:
        rows = [arms[arm] for arms in cycles.values()]
        speeds = [r["mibPerSec"] for r in rows]
        pair_ratios = [arms[arm]["mibPerSec"] / arms["range-1"]["mibPerSec"] for arms in cycles.values()]
        summaries[arm] = {
            "samples": len(rows),
            "medianMiBPerSec": statistics.median(speeds),
            "minMiBPerSec": min(speeds),
            "maxMiBPerSec": max(speeds),
            "medianSeconds": statistics.median(r["elapsedMS"] / 1000 for r in rows),
            "pairedSpeedupsVsOne": pair_ratios,
            "medianPairedSpeedupVsOne": statistics.median(pair_ratios),
            "peaks": [r["peakRequests"] for r in rows],
            "medianActiveRequests": statistics.median(r["meanActiveRequests"] for r in rows),
            "medianRequestHeadersMS": statistics.median(o["headersMS"] for r in rows for o in r["requests"]),
            "medianRequestBodyAndValidationMS": statistics.median(o["elapsedMS"] - o["headersMS"] for r in rows for o in r["requests"]),
            "medianHeadersShareOfRequestTime": statistics.median(
                sum(o["headersMS"] for o in r["requests"]) / sum(o["elapsedMS"] for o in r["requests"])
                for r in rows),
            "reusedRequests": sum(o["reused"] for r in rows for o in r["requests"]),
            "requests": sum(len(r["requests"]) for r in rows),
        }
    four_vs_three = [arms["range-4"]["mibPerSec"] / arms["range-3"]["mibPerSec"] for arms in cycles.values()]
    result = {
        "scope": "Single direct route, one content prefix and one observation period; exploratory, not game acceptance",
        "host": signatures[0][0], "ip": signatures[0][1], "song": signatures[0][2],
        "resourceBytes": signatures[0][3], "intervalBytes": signatures[0][4],
        "sessionsHaveIdenticalReferenceHashes": True,
        "protocols": sorted(protocols), "cacheStates": sorted(cache_states),
        "colos": sorted(colos), "servers": sorted(servers),
        "downloadedBodyBytesExcludingPreflight": transferred,
        "observedRequestsExcludingPreflight": requests,
        "warmupRuns": len(warmups), "primaryRuns": len(selected), "excludedRuns": len(excluded),
        "excludedRoundKeys": sorted(exclude),
        "summaries": summaries,
        "pairedFourVsThree": four_vs_three,
        "medianPairedFourVsThree": statistics.median(four_vs_three),
        "rounds": [{"key": key, "arms": {arm: {
            "mibPerSec": arms[arm]["mibPerSec"], "seconds": arms[arm]["elapsedMS"] / 1000,
            "peakRequests": arms[arm]["peakRequests"]} for arm in ARMS}} for key, arms in cycles.items()],
    }
    return result, selected


def plot(result, selected, destination):
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    fig, ax = plt.subplots(figsize=(9.5, 5.5), layout="constrained")
    colors = ["#375A7F", "#2980B9", "#179C8A", "#D78B22", "#8056A0"]
    for i, arm in enumerate(ARMS):
        values = [r["mibPerSec"] for r in selected if r["arm"] == arm]
        median = statistics.median(values)
        ax.bar(i, median, color=colors[i], alpha=.22, width=.62)
        for j, value in enumerate(values):
            ax.scatter(i + (j - (len(values)-1)/2)*.09, value, color=colors[i], s=45, zorder=3)
        ax.plot([i-.25, i+.25], [median, median], color=colors[i], linewidth=3)
        ax.text(i, max(values)+.18, f"{median:.2f}", ha="center", fontweight="bold", color=colors[i])
    ax.set_xticks(range(5), ["1 Range", "2 Ranges", "3 Ranges\n(current)", "4 Ranges", "1 long Range\n(control)"])
    ax.set_ylabel("Verified throughput (MiB/s)")
    ax.set_title("CF prefix download: concurrent 1 MiB ranges", loc="left", fontweight="bold")
    ax.text(0, 1.02, "30 MiB / fixed direct IP / HTTP/1.1 / SEA / cache HIT", transform=ax.transAxes, fontsize=10)
    ax.set_ylim(0, max(r["mibPerSec"] for r in selected)+.9)
    ax.grid(axis="y", alpha=.2)
    ax.spines[["top", "right"]].set_visible(False)
    fig.supxlabel("Dots: four paired rounds. Bar/line: median. One potentially confounded round was retained but excluded.", fontsize=9)
    destination.parent.mkdir(parents=True, exist_ok=True)
    fig.savefig(destination, dpi=160)
    plt.close(fig)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("inputs", type=Path, nargs="+")
    parser.add_argument("--exclude-round", action="append", default=[])
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--chart", type=Path)
    args = parser.parse_args()
    result, selected = summarize(args.inputs, set(args.exclude_round))
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    if args.chart:
        plot(result, selected, args.chart)
    print(json.dumps({"arms": result["summaries"], "fourVsThree": result["pairedFourVsThree"]}, indent=2))


if __name__ == "__main__":
    main()
