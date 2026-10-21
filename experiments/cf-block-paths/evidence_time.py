"""Normalize RFC3339 timestamps in already-curated CF evidence, not private logs."""
import argparse
import json
import re
from datetime import datetime, timezone
from pathlib import Path

STAMP = re.compile(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})")
TIME_BASIS = ("T+seconds relative to the earliest nonzero timestamp in this file; "
              "unset timestamps are null; baselines across files are independent")


def relative_times(data):
    """Preserve nanosecond differences, metrics and hashes; never export the epoch."""
    stamps = {}

    def visit(value):
        if isinstance(value, dict):
            for v in value.values():
                visit(v)
        elif isinstance(value, list):
            for v in value:
                visit(v)
        elif isinstance(value, str) and (m := STAMP.fullmatch(value)):
            if m[1] == "0001-01-01T00:00:00":
                stamps[value] = None  # Go's unset time is not a real observation.
            else:
                dt = datetime.fromisoformat(m[1] + m[3])
                delta = dt - datetime(1970, 1, 1, tzinfo=timezone.utc)
                stamps[value] = ((delta.days * 86400 + delta.seconds) * 10**9
                                 + int((m[2] or "0").ljust(9, "0")))

    visit(data)
    base = min((v for v in stamps.values() if v is not None), default=0)

    def replace(value):
        if isinstance(value, dict):
            return {k: replace(v) for k, v in value.items()}
        if isinstance(value, list):
            return [replace(v) for v in value]
        if isinstance(value, str) and value in stamps:
            if stamps[value] is None:
                return None
            seconds, fraction = divmod(stamps[value] - base, 10**9)
            return f"T+{seconds}.{fraction:09d}".rstrip("0").rstrip(".") + "s"
        return value

    result = replace(data)
    if stamps:
        result["timeBasis"] = TIME_BASIS
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("files", nargs="+", type=Path)
    for path in parser.parse_args().files:
        data = relative_times(json.loads(path.read_bytes()))
        path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n",
                        encoding="utf-8", newline="\n")
