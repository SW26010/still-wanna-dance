"""Join candidate membership with DNS lease and neighboring API/TLS evidence."""
import argparse
import collections
import datetime
import hashlib
import json
from pathlib import Path


def at(value):
    return datetime.datetime.fromisoformat(value)


def classify(event, lookups):
    state = event["After"]
    when = at(state["At"])
    live = [ip for ip, expiry in (state.get("Leases") or {}).items() if at(expiry) > when]
    prior = [r for r in lookups if r["Host"] == event["Host"] and at(r["Started"]) <= at(state["Observed"])]
    last = max(prior, key=lambda r: at(r["Started"])) if prior else None
    expired = at(state["CacheUntil"]) <= when
    if event.get("IPs"):
        label = "available"
    elif live:
        label = "snapshot_race_or_other"
    elif expired and last and last["TTLMS"] == 0 and not last["Error"]:
        label = "zero_ttl"
    elif expired and state["LookupPending"]:
        label = "expired_refresh_pending"
    elif expired and state.get("CacheIPs"):
        label = "positive_ttl_expired"
    else:
        label = "other_or_initial"
    return {"classification": label, "lastObservedLookupID": last["ID"] if last else None,
            "lastObservedTTLMS": last["TTLMS"] if last else None,
            "liveLeasesAtSnapshot": live,
            "cacheRemainingMS": (at(state["CacheUntil"]) - when).total_seconds()*1000}


def summarize_discovery(path):
    raw = path.read_bytes()
    data = json.loads(raw)
    events = data["Events"]
    assert all(r["Host"] in ("api.udon.dance", "play.udon.dance") for r in events)
    anomalies = [{"event": i, **e, **classify(e, data["Lookups"])} for i, e in enumerate(events) if not e.get("IPs")]
    tls = []
    for check in data.get("TLS") or []:
        event = events[check["Event"]]
        assert event["Host"] == check["Host"] and not event.get("IPs")
        assert not check["OK"] or check["Remote"] == check["IP"]
        tls.append({**check, "afterEventMS": (at(check["Started"]) - at(event["After"]["At"])).total_seconds()*1000})
    monitors = []
    for source in sorted(path.parent.glob("monitor-*-private.json")):
        content = source.read_bytes()
        m = json.loads(content)
        rows = [r for r in m["results"] if r["operation"] == "playback_url" and r["route"] == "cf"]
        requests = [r for r in data.get("Requests") or [] if at(r["Started"]) >= at(m["started"]) and at(r["Finished"]) <= at(m["finished"])]
        monitors.append({"sourceSHA256": hashlib.sha256(content).hexdigest(), "started": m["started"], "finished": m["finished"],
                         "playbackResults": [{k: r.get(k) for k in ("ip", "state", "reason", "http")} for r in rows],
                         "requests": len(requests), "redirectResponses": sum(r["Status"] in (301, 302, 307, 308) for r in requests),
                         "failedRequests": sum(bool(r["Error"]) for r in requests)})
    host_counts = {}
    for host in ("api.udon.dance", "play.udon.dance"):
        discoveries = [e for e in events if e["Host"] == host and e["Operation"] == "discovery"]
        host_counts[host] = {"discoveryCalls": len(discoveries), "emptyDiscoveryCalls": sum(not e.get("IPs") for e in discoveries),
                            "discoveryErrors": sum(bool(e["Error"]) for e in discoveries),
                            "emptyCurrentReads": sum(e["Host"] == host and e["Operation"] == "current_empty" for e in events)}
    return {"schema": 1, "sourceSHA256": hashlib.sha256(raw).hexdigest(), "started": data["Started"], "saved": data["Saved"],
            "outcome": data["Outcome"], "hostCounts": host_counts, "emptyEvents": anomalies,
            "emptyEventClasses": dict(collections.Counter(e["classification"] for e in anomalies)),
            "tls": tls, "monitors": monitors, "requests": data.get("Requests") or [],
            "requestOutcomes": dict(collections.Counter(r["Error"] or str(r["Status"]) for r in data.get("Requests") or [])),
            "lookups": data["Lookups"], "answers": data["Answers"],
            "zeroTTLSuccessfulLookups": sum(r["TTLMS"] == 0 and not r["Error"] for r in data["Lookups"]),
            "interpretation": "Read-only Current expiry is separate from refreshing Candidates failure. Adjacent TLS success does not retroactively explain the earlier four-arm interruption."}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    result = summarize_discovery(args.source)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2)+"\n", encoding="utf-8")
    print(json.dumps({k: v for k, v in result.items() if k not in ("emptyEvents", "requests", "answers", "lookups", "monitors")}, ensure_ascii=False, indent=2))
