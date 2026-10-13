"""Export, verify and summarize the bounded local CPU follow-up experiment."""
import argparse
import csv
import gzip
import hashlib
import json
from pathlib import Path
import re
import statistics
import subprocess

import analyze_cpu as cpu

REPO=Path(__file__).resolve().parents[2]
ROOT=REPO/"artifacts/cf-cpu-followup"
OUT=REPO/"docs/evidence/cf-cpu-followup-20261014"
XPERF=Path(r"C:\Program Files (x86)\Windows Kits\10\Windows Performance Toolkit\xperf.exe")

def save(path,value):
    path.write_text(json.dumps(value,indent=2)+"\n",encoding="utf-8")

def gzip_json(path,value):
    path.write_bytes(gzip.compress(json.dumps(value,separators=(",",":")).encode(),mtime=0))

def public_report(r):
    r=dict(r);r.pop("pid",None)
    r["plan"]={k:v for k,v in r["plan"].items() if k not in ("OriginInfo","Out","Storage")}
    # Any error from private filesystem operations is deliberately not published.
    if r.get("error"):r["error"]="see private failure log"
    return r

def export_and_analyze():
    OUT.mkdir(exist_ok=True,parents=True)
    summaries=[];reports=[];checks=[];warnings=[]
    design=json.loads((ROOT/"plan.json").read_text())
    for repetition in range(1,6):
        stem=f"follow-r{repetition}";etl=ROOT/(stem+".etl");dump=ROOT/(stem+".csv")
        if not dump.exists():
            with (ROOT/(stem+"-export-private.log")).open("w") as log:
                subprocess.run([str(XPERF),"-i",str(etl),"-o",str(dump),"-a","dumper"],stdout=log,stderr=log,check=True)
        group=[json.loads((ROOT/(arm["ID"]+".json")).read_text()) for arm in design["rounds"][repetition-1]]
        export_log=(ROOT/(stem+"-export-private.log")).read_text(errors="replace")
        warnings.append(dict(trace=stem,invalidEventWarnings=export_log.lower().count("invalid event"),note="Other kernel event decoding warnings are distinct from ETW event/buffer loss"))
        assert len({r["pid"] for r in group})==len(group),"PID reused within one trace"
        print(f"parsing {stem}",flush=True)
        for r,trace in cpu.extract(dump,group):
            packed=cpu.pack_trace(trace)
            unpacked=cpu.unpack_trace(packed)
            for key in ("intervals","ready","switchUS","systemInterrupts"):
                assert sorted(unpacked[key])==sorted(trace[key]),"lossy trace encoding"
            gzip_json(OUT/(r["id"]+"-scheduling.json.gz"),packed)
            s=cpu.analyze_report(r,trace)
            s.update(mode=r["plan"]["Mode"],scenario=r["plan"]["Scenario"],warm=r["plan"]["Warm"],round=repetition,
                     peakRequests=r["PeakRequests"],connections=r["Connections"],tlsHandshakes=r["TLSHandshakes"],
                     firstBodyMS=r["FirstBodyMS"],seekMS=r["SeekMS"],completeMS=r["CompleteMS"],closeMS=r["CloseMS"],
                     cancelWaitMS=r["CancelWaitMS"],publishedFiles=r["PublishedFiles"],
                     originBytes=r["OriginStats"]["bytes"],originPeak=r["OriginStats"]["peak"],
                     budgetTotal=r["BudgetTotal"],budgetBackground=r["BudgetBackground"],workers=r["BackgroundWorkers"],
                     peakPrivateMiB=max(v["PrivateBytes"] for v in r["samples"])/2**20,
                     peakWorkingSetMiB=max(v["WorkingSetBytes"] for v in r["samples"])/2**20,
                     peakHeapMiB=max(v["HeapBytes"] for v in r["samples"])/2**20,
                     peakHandles=max(v["Handles"] for v in r["samples"]),endHandles=r["samples"][-1]["Handles"],
                     firstHandles=r["samples"][0]["Handles"])
            passed=r["ok"] and r["ActiveAfterClose"]==0 and r["PartialsAfterClose"]==0 and r["OriginStats"]["active"]==0
            if s["mode"]=="raw":
                passed &= r["PeakRequests"]==r["target"] and r["OriginStats"]["bytes"]==96*2**20
                passed &= sum(o["bytes"] for o in r["requests"])==96*2**20
                passed &= len(r["requests"])==96 and all(o["Status"]==206 and not o["Failed"] for o in r["requests"])
                passed &= r["TLSHandshakes"]==(0 if s["warm"] else r["target"])
            elif s["mode"]=="engine":
                passed &= r["PeakRequests"]<=r["BudgetTotal"]
                passed &= r["PublishedFiles"]==dict(prefetch=1,compete=3,cancel=0)[s["scenario"]]
            assert passed,"trial check failed: "+r["id"]
            checks.append(dict(id=r["id"],passed=bool(passed),eventsLost=trace["eventsLost"],buffersLost=trace["buffersLost"],losslessTrace=True))
            summaries.append(s);reports.append(public_report(r))
        save(OUT/"summary.json",summaries)
        print(f"verified {stem}: {len(group)} groups",flush=True)
    gzip_json(OUT/"trials.json.gz",reports)
    save(OUT/"checks.json",dict(allPassed=True,trials=checks,exportWarnings=warnings))
    save(OUT/"plan.json",design)
    save(OUT/"source.json",json.loads((ROOT/"source.json").read_text()))
    save(OUT/"builds.json",json.loads((ROOT/"builds.json").read_text()))
    save(OUT/"helper-check.json",json.loads((ROOT/"helper-check.json").read_text()))
    preliminary=[public_report(json.loads(path.read_text())) for pattern in ("pilot-*.json","race-*.json") for path in ROOT.glob(pattern) if "requests" in json.loads(path.read_text())]
    gzip_json(OUT/"preliminary.json.gz",preliminary)
    save(OUT/"aggregates.json",aggregate(summaries))

def aggregate(rows):
    result=[]
    for mode in ("raw","engine"):
        variants=[("cold",False),("warm",True)] if mode=="raw" else [(s,False) for s in ("prefetch","compete","cancel")]
        for variant,warm in variants:
            for n in (4,8,16,32):
                group=[r for r in rows if r["mode"]==mode and r["target"]==n and (r["warm"]==warm if mode=="raw" else r["scenario"]==variant)]
                if len(group)!=5:continue
                v=dict(mode=mode,variant=variant,n=n,repetitions=len(group))
                metrics={"elapsedMS":[r["windows"]["full"]["endMS"] for r in group],
                         "cpuMS":[r["windows"]["full"]["cpuMS"] for r in group],
                         "cpuMachinePercent":[r["windows"]["full"]["machinePercent"] for r in group],
                         "throughputMiB":[r["originBytes"]/2**20/(r["windows"]["full"]["endMS"]/1000) for r in group],
                         "peak10msPercent":[r["windows"]["full"]["peak10msCores"]*100/16 for r in group],
                         "peak100msPercent":[r["windows"]["full"]["peak100msCores"]*100/16 for r in group],
                         "simultaneousLogical":[r["windows"]["full"]["maxSimultaneousLogical"] for r in group],
                         "simultaneousPhysical":[r["windows"]["full"]["maxSimultaneousPhysical"] for r in group]}
                for key in ("firstBodyMS","seekMS","completeMS","closeMS","cancelWaitMS","peakPrivateMiB","peakHeapMiB","peakHandles","peakRequests","connections","tlsHandshakes"):
                    metrics[key]=[r[key] for r in group]
                for key,values in metrics.items():v[key]=dict(median=statistics.median(values),min=min(values),max=max(values),values=values)
                result.append(v)
    return result

def crosscheck():
    rows=json.loads((OUT/"summary.json").read_text());checks=[]
    # First and last repetition, every mode and level: 42 independent totals.
    for s in rows:
        if s["round"] not in (1,5):continue
        r=json.loads((ROOT/(s["id"]+".json")).read_text())
        tr=json.loads(gzip.decompress((OUT/(s["id"]+"-scheduling.json.gz")).read_bytes()))
        path=ROOT/(s["id"]+"-crosscheck-private.txt")
        with (ROOT/"crosscheck-private.log").open("a") as log:
            subprocess.run([str(XPERF),"-i",str(ROOT/f'follow-r{s["round"]}.etl'),"-o",str(path),"-a","cswitch","-process","-exc_dpcisr","-range",str(tr["traceBeginUS"]),str(tr["traceBeginUS"]+tr["durationUS"])],stdout=log,stderr=log,check=True)
        match=re.search(r"^\s*(\d+),\s*follow-\d+\.exe\s*\(\s*"+str(r["pid"])+r"\)",path.read_text(errors="replace"),re.M)
        assert match,"missing xperf total"
        reference=int(match[1]);actual=round(s["windows"]["full"]["cpuMS"]*1000);difference=abs(reference-actual)
        assert difference<=max(500,reference*.005),f'crosscheck mismatch {s["id"]}'
        checks.append(dict(id=s["id"],xperfCPUUS=reference,parsedCPUUS=actual,absoluteDifferenceUS=difference))
        print(f'crosschecked {s["id"]}: {difference} us',flush=True)
    save(OUT/"crosschecks.json",dict(allPassed=True,checks=checks))

if __name__=="__main__":
    p=argparse.ArgumentParser();p.add_argument("action",choices=("analyze","crosscheck"));a=p.parse_args()
    if a.action=="analyze":export_and_analyze()
    else:crosscheck()
