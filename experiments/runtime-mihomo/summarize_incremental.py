"""Descriptive measurements, with sample counts and failures; no selection rule."""
import argparse
import json
from pathlib import Path
import statistics


def distribution(values):
    return {"count":len(values),"median":statistics.median(values),"min":min(values),"max":max(values)} if values else {"count":0,"median":None,"min":None,"max":None}


def main():
    ap=argparse.ArgumentParser();ap.add_argument("result",type=Path);ap.add_argument("--out",type=Path,required=True)
    args=ap.parse_args();data=json.loads(args.result.read_text(encoding="utf-8"))
    out={"scope":"finite development measurements; no architecture selected", "binaries":data["binaries"],
         "checks":{"total":len(data["checks"]),"passed":sum(x["passed"] for x in data["checks"])},
         "local":{},"real":{},"admissionPolicy":data.get("admissionPolicy"),"cleanup":data["cleanup"]}
    for mode in ("B","B2","C","D"):
        rows=[x for x in data["benchmarks"] if x["mode"]==mode]
        out["local"][mode]={"rounds":len(rows),
            "throughputMiBps":distribution([128/x["bulkWallSeconds"] for x in rows]),
            "cpuSecondsPer128MiB":distribution([x["cpuSeconds"] for x in rows]),
            "idleTotalWorkingSetBytes":distribution([sum(s["workingSetBytes"] for s in x["idle"]) for x in rows]),
            "readyMS":distribution([x["startupMS"] for x in rows]),
            "failedRequests":sum(not v["ok"] for x in rows for key in ("cold","warm","bulk") for v in x[key])}
        rows=[x for x in data.get("realBenchmarks",[]) if x["mode"]==mode]
        out["real"][mode]={}
        for kind in ("cold","warm"):
            attempts=[v for x in rows for v in x[kind]]
            success=[x for x in attempts if x["ok"]]
            out["real"][mode][kind]={"attempts":len(attempts),"failures":len(attempts)-len(success),
                "elapsedMSAllAttempts":distribution([x["elapsedMS"] for x in attempts]),
                "firstByteMSSuccessful":distribution([x["firstByteMS"] for x in success]),
                "connectionMSSuccessful":distribution([x["connectionMS"] for x in success]),
                "responseAfterConnectionMSSuccessful":distribution([x["firstByteMS"]-x["connectionMS"] for x in success]),
                "reusedConnections":sum(x["reused"] for x in attempts)}
    out["soak"]=data["soak"]
    out["otherMatrices"]={}
    for label in ("lifecycle","attribution"):
        path=args.result.parent/(label+".json")
        if path.exists():
            other=json.loads(path.read_text(encoding="utf-8"))
            out["otherMatrices"][label]={"source":path.name,"total":len(other["checks"]),
                "conditionsMet":sum(x["passed"] for x in other["checks"]),
                "unmetChecks":[x["name"] for x in other["checks"] if not x["passed"]]}
    out["limitations"]=["Three local rounds and at most six cold/six warm samples per mode; no population inference",
        "Cold means first request per channel in a fresh runtime; second channel may reuse core DNS cache",
        "GotConn for B1 is a pipe, and for C/D a local SOCKS connection; it need not mark remote handshake completion",
        "C includes driver and core working sets; excludes fixture; startup includes controller readiness polling",
        "Local fixture/provider structure and scheduling differ; D does not isolate process cost perfectly",
        "No game, long-session, release, DPAPI or whole-product acceptance claim"]
    args.out.write_text(json.dumps(out,indent=2)+"\n",encoding="utf-8",newline="\n")
    print(json.dumps({"checks":out["checks"],"modes":list(out["local"])}))


if __name__=="__main__":main()
