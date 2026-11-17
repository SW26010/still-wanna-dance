"""Independent PyAV demux check of extremes and deterministic spread samples.

Usage: python verify-media-map.py <inventory.json> <scan-dir> <result.json>
Requires PyAV; does not rewrite video or publish paths/resource keys.
"""
import json
import math
import sys
from pathlib import Path

import av

inventory_path, scan_dir, output = map(Path, sys.argv[1:])
inventory = json.loads(inventory_path.read_text(encoding="utf-8-sig"))
paths = {item["key"]: item["file"] for item in inventory["files"]}
rows = [json.loads(line) for part in sorted(scan_dir.glob("part-*.jsonl"))
        for line in part.read_text(encoding="utf-8").splitlines()]
files = sorted((r for r in rows if r.get("catalogSeconds")), key=lambda r: r["key"])
block_bytes = 1048576
extremes = []
for row in files:
    errors = [row["catalogSeconds"] * b * block_bytes / row["bytes"] - p[0]
              for b, p in enumerate(row["blocks"]) if b and p[0] is not None]
    if errors:
        extremes.append((row, min(errors), max(errors)))
chosen = {}
for row, _, _ in (sorted(extremes, key=lambda x: x[1])[:3]
                  + sorted(extremes, key=lambda x: -x[2])[:3]):
    chosen[row["key"]] = row
for row in [r for r in files if not r["progressive"]][:3]:
    chosen[row["key"]] = row
for i in range(24):
    row = files[math.floor(i * (len(files) - 1) / 23)]
    chosen[row["key"]] = row

results = []
for row in chosen.values():
    # Independently use FFmpeg's edited presentation/decode timestamps and packet offsets.
    first = [[math.inf] * 4 for _ in row["blocks"]]
    sync = []
    packets = 0
    with av.open(paths[row["key"]]) as container:
        for packet in container.demux():
            if packet.stream.type not in ("audio", "video") or packet.pos is None or not packet.size:
                continue
            pts = float(packet.pts * packet.time_base)
            dts = float(packet.dts * packet.time_base)
            end = pts + float(packet.duration * packet.time_base)
            if end <= 0 or pts >= row["containerSeconds"]:
                continue
            packets += 1
            pts = max(0, pts)
            is_video = packet.stream.type == "video"
            if is_video and packet.is_keyframe:
                sync.append((packet.pos, pts))
            for b in range(packet.pos // block_bytes, (packet.pos + packet.size - 1) // block_bytes + 1):
                p = first[b]
                p[0] = min(p[0], pts)
                p[1] = min(p[1], dts)
                col = 2 if is_video else 3
                p[col] = min(p[col], pts)
    sync.sort()
    cursor = 0
    mismatch = 0
    max_error = 0.0
    max_timeline_error = 0.0
    timeline_mismatches = 0
    sync_mismatches = 0
    details = []
    for b, expected in enumerate(row["blocks"]):
        for col, expected_col in enumerate((0, 1, 3, 5)):
            actual = first[b][col]
            wanted = expected[expected_col]
            if wanted is None:
                mismatch += math.isfinite(actual)
                timeline_mismatches += math.isfinite(actual)
            elif not math.isfinite(actual):
                mismatch += 1
                timeline_mismatches += 1
            else:
                error = abs(actual - wanted)
                max_error = max(max_error, error)
                max_timeline_error = max(max_timeline_error, error)
                mismatch += error > 0.00001
                timeline_mismatches += error > 0.00001
                if error > 0.00001:
                    details.append(dict(block=b, column=expected_col, index=wanted, demux=actual))
        while cursor < len(sync) and sync[cursor][0] < min(row["bytes"], (b + 1) * block_bytes):
            cursor += 1
        actual = sync[cursor][1] if cursor < len(sync) else None
        wanted = expected[7]
        if actual is None or wanted is None:
            mismatch += actual != wanted
            sync_mismatches += actual != wanted
        else:
            max_error = max(max_error, abs(actual - wanted))
            mismatch += abs(actual - wanted) > 0.00001
            sync_mismatches += abs(actual - wanted) > 0.00001
            if abs(actual - wanted) > 0.00001:
                details.append(dict(block=b, column=7, index=wanted, demux=actual))
    result = dict(songs=row["songIDs"], packets=packets, blocks=len(first),
                  mismatches=mismatch, timelineMismatches=timeline_mismatches,
                  syncMismatches=sync_mismatches, maxTimelineErrorSeconds=max_timeline_error,
                  maxTimeErrorSeconds=max_error, details=details)
    results.append(result)
    print(json.dumps(result), flush=True)

output.write_text(json.dumps(dict(
    selection="Three most negative files, three most positive, first three tail-metadata files, and 24 evenly spaced files sorted by private resource key; deduplicated.",
    toleranceSeconds=0.00001, pyav=av.__version__, libraries=av.library_versions,
    results=results), indent=2) + "\n", encoding="utf-8")
sys.exit(1 if any(r["mismatches"] for r in results) else 0)
