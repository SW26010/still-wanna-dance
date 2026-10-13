"""Extract only target-process scheduling intervals from private xperf dumps.

All timestamps are integer microseconds. DPC/ISR intervals are unioned and
subtracted from thread residency (including nested interrupts only once).
"""
import argparse
from bisect import bisect_right
from collections import Counter, defaultdict
import csv
import gzip
import json
from pathlib import Path
import re
import statistics

PROVIDER = "{e3d074a8-25fb-4d47-9427-3bc102cf5d31}"

def pid(value):
    return int(re.search(r"\(\s*(-?\d+)\)", value).group(1))

def union(intervals):
    result = []
    for a,b in sorted(intervals):
        if b <= a: continue
        if result and a <= result[-1][1]: result[-1][1] = max(b, result[-1][1])
        else: result.append([a,b])
    return result

def subtract(a,b,excluded,ends):
    i = bisect_right(ends,a)
    while i < len(excluded) and excluded[i][0] < b:
        x,y = excluded[i]
        if x>a: yield a,min(b,x)
        a = max(a,y)
        if a>=b: return
        i+=1
    if a<b: yield a,b

def extract(path, reports):
    targets = {r["pid"]:r for r in reports}
    intervals = defaultdict(list)
    markers = defaultdict(list)
    threads = defaultdict(set)
    ready = defaultdict(list)
    switches = defaultdict(list)
    interrupts = defaultdict(list)
    current = {}
    ready_at = {}
    lost = None
    mismatches = 0
    with path.open(encoding="utf-8-sig",errors="replace",newline="") as f:
        for row in csv.reader(f,skipinitialspace=True):
            if not row: continue
            row = [v.strip() for v in row]
            name = row[0]
            if name.startswith("OS Version:"):
                text = ",".join(row)
                lost = [int(re.search(r"Events Lost:\s*(\d+)",text)[1]), int(re.search(r"Buffers lost:\s*(\d+)",text)[1])]
                continue
            if len(row)<2 or not row[1].isdigit(): continue
            ts = int(row[1])
            if name == "CSwitch":
                cpu, tid, process = int(row[16]),int(row[3]),pid(row[2])
                oldtid,oldprocess = int(row[9]),pid(row[8])
                previous=current.get(cpu)
                if previous:
                    start,p,t = previous
                    if p in targets:
                        if t != oldtid: mismatches+=1
                        if ts>start: intervals[p].append((start,ts,t,cpu))
                if oldprocess in targets and row[12] == "Ready": ready_at[oldtid]=ts
                if process in targets and tid in ready_at:
                    start=ready_at.pop(tid)
                    if ts>=start: ready[process].append((ts,ts-start))
                current[cpu]=(ts,process,tid)
                if process in targets: switches[process].append(ts)
            elif name in ("DPC","DPCTmr","Interrupt"):
                elapsed,cpu=int(row[2]),int(row[3])
                if elapsed>0: interrupts[cpu].append((ts-elapsed,ts))
            elif name == "ReadyThread" and pid(row[4]) in targets:
                ready_at.setdefault(int(row[5]),ts)
            elif name in ("T-Start","T-DCStart") and pid(row[2]) in targets:
                threads[pid(row[2])].add(int(row[3]))
            elif name=="UnknownEvent/Crimson" and row[5].lower()==PROVIDER and pid(row[2]) in targets:
                markers[pid(row[2])].append(ts)
    if lost != [0,0] or mismatches:
        raise ValueError(f"unreliable scheduling trace: lost={lost}, mismatches={mismatches}")
    excluded={cpu:union(v) for cpu,v in interrupts.items()}
    ends={cpu:[b for a,b in v] for cpu,v in excluded.items()}
    for process,r in targets.items():
        marks=markers[process]
        if len(marks)!=len(r["stages"])+3: raise ValueError("missing phase markers")
        zero,stop,end=marks[0],marks[-2],marks[-1]
        if abs((end-zero)/1000-r["endMS"])>3: raise ValueError("trace/worker clock alignment mismatch")
        names={t:i+1 for i,t in enumerate(sorted(threads[process]))}
        clean=[];raw=0
        for a,b,t,cpu in intervals[process]:
            a,b=max(a,zero),min(b,end)
            if b<=a:continue
            raw+=b-a
            if t not in names: raise ValueError("missing thread lifetime")
            for x,y in subtract(a,b,excluded.get(cpu,[]),ends.get(cpu,[])):
                clean.append([x-zero,y-zero,names[t],cpu])
        system_interrupts=[[max(a,zero)-zero,min(b,end)-zero,cpu] for cpu,v in excluded.items() for a,b in v if a<end and b>zero]
        yield r,dict(id=r["id"],durationUS=end-zero,stopUS=stop-zero,stageUS=[m-zero for m in marks[1:-2]],threadsCreated=len(names),intervals=clean,ready=[[a-zero,b] for a,b in ready[process] if zero<=a<=end],switchUS=[a-zero for a in switches[process] if zero<=a<=end],systemInterrupts=system_interrupts,rawScheduledUS=raw,interruptOverlapUS=raw-sum(b-a for a,b,t,c in clean),eventsLost=0,buffersLost=0)

def percentile(values,p):
    if not values:return 0
    values=sorted(values);return values[min(len(values)-1,int((len(values)-1)*p))]

def window(trace,a,b,logical,core_masks):
    segments=[(max(a,x),min(b,y),t,c) for x,y,t,c in trace["intervals"] if x<b and y>a]
    events=defaultdict(int);per_cpu=Counter();per_thread=Counter();per_core=Counter()
    core_of={i:j for j,m in enumerate(core_masks) for i in range(logical) if m&(1<<i)}
    bins=[0.0]*int((b-a+99999)//100000)
    core_events=defaultdict(list)
    for x,y,t,c in segments:
        events[x]+=1;events[y]-=1;per_cpu[c]+=y-x;per_thread[t]+=y-x;per_core[core_of[c]]+=y-x
        core_events[x].append((core_of[c],1));core_events[y].append((core_of[c],-1))
        for i in range((x-a)//100000,(y-a-1)//100000+1):
            bins[i]+=max(0,min(y,a+(i+1)*100000)-max(x,a+i*100000))
    histogram=Counter();count=0;previous=a;peak=0
    for ts,delta in sorted(events.items()):
        histogram[count]+=ts-previous;count+=delta;peak=max(peak,count);previous=ts
    histogram[count]+=b-previous
    physical_count=Counter();physical_peak=0
    for ts,changes in sorted(core_events.items()):
        for c,d in changes:physical_count[c]+=d
        physical_peak=max(physical_peak,sum(v>0 for v in physical_count.values()))
    total=sum(per_cpu.values());duration=b-a
    # A final fractional bin must not masquerade as a 100 ms peak.
    complete_bins=bins[:duration//100000]
    windows100=[v/100000 for v in complete_bins]
    windows1s=[sum(complete_bins[i:i+10])/1000000 for i in range(len(complete_bins)-9)]
    ready=[d for ts,d in trace["ready"] if a<=ts<b]
    interrupt_us=sum(max(0,min(b,y)-max(a,x)) for x,y,c in trace.get("systemInterrupts",[]))
    return dict(startMS=a/1000,endMS=b/1000,cpuMS=total/1000,coreEquivalent=total/duration,machinePercent=100*total/duration/logical,peak100msCores=max(windows100,default=0),p95_100msCores=percentile(windows100,.95),peak1sCores=max(windows1s,default=0),maxSimultaneousLogical=peak,maxSimultaneousPhysical=physical_peak,logicalTouched=len(per_cpu),physicalTouched=len(per_core),activeThreads=len(per_thread),cpuByLogicalMS={str(k):v/1000 for k,v in sorted(per_cpu.items())},cpuByThreadMS={str(k):v/1000 for k,v in sorted(per_thread.items())},runningPercent={str(k):100*v/duration for k,v in sorted(histogram.items())},twoOrMorePercent=100*sum(v for k,v in histogram.items() if k>=2)/duration,fourOrMorePercent=100*sum(v for k,v in histogram.items() if k>=4)/duration,readyP95US=percentile(ready,.95),readyMaxUS=max(ready,default=0),readyTransitions=len(ready),contextSwitches=sum(a<=ts<b for ts in trace.get("switchUS",[])),systemInterruptMS=interrupt_us/1000,systemInterruptMachinePercent=100*interrupt_us/duration/logical,series100msCores=windows100)

def analyze_report(r,t):
    masks=r["coreMasks"]
    windows={"active":(0,t["stopUS"]),"full":(0,t["durationUS"]),"startup":(0,min(1000000,t["stopUS"]))}
    if r["kind"]=="stepped":
        for i,s in enumerate(r["stages"]):
            begin=t["stageUS"][i];end=t["stageUS"][i+1] if i+1<len(t["stageUS"]) else t["stopUS"]
            windows[f"steady-{s['n']}"]=(begin+2000000,end)
    else:
        windows["steady"]=(t["stageUS"][-1]+2000000,t["stopUS"])
        if r["kind"]=="stagger":windows["ramp"]=(0,t["stageUS"][-1])
    result=dict(id=r["id"],kind=r["kind"],target=r["target"],ok=r["ok"],threadsCreated=t["threadsCreated"],goroutinesPeak=max(s["goroutines"] for s in r["samples"]),gcs=r["gcs"],gcPauseMS=r["gcPauseMS"],allocatedMiB=r["allocBytes"]/2**20,drainMS=(t["durationUS"]-t["stopUS"])/1000,interruptOverlapMS=t["interruptOverlapUS"]/1000,windows={})
    requests=r["requests"] or []
    for name,(a,b) in windows.items():
        if b<=a:continue
        w=window(t,a,b,r["logicalCPUs"],masks)
        # Completed-block throughput, not interpolated wire bytes.
        bytecount=sum(o["bytes"] for o in requests if a/1000<=o["startMS"]+o["elapsedMS"]<b/1000)
        if name=="full":bytecount=sum(o["bytes"] for o in requests)
        w["completedMiB"]=bytecount/2**20;w["mibPerSec"]=bytecount/2**20/((b-a)/1e6)
        w["cpuSecondsPerGiB"]=w["cpuMS"]/1000/(bytecount/2**30) if bytecount else None
        result["windows"][name]=w
    samples=r["samples"];result["processUserMS"]=samples[-1]["userMS"]-samples[0]["userMS"];result["processKernelMS"]=samples[-1]["kernelMS"]-samples[0]["kernelMS"]
    return result

def main():
    p=argparse.ArgumentParser();p.add_argument("--root",type=Path,default=Path("artifacts/cf-cpu"));p.add_argument("--out",type=Path,required=True);args=p.parse_args()
    args.out.mkdir(parents=True,exist_ok=True)
    reports=[];summaries=[]
    for round_no in (1,2,3):
        group=[json.loads(f.read_text()) for f in sorted(args.root.glob(f"r{round_no}-*.json"))]
        for r,t in extract(args.root/f"cpu-main-r{round_no}.csv",group):
            summaries.append(analyze_report(r,t))
            public=dict(r);public.pop("pid");
            for o in public["requests"] or []:o.pop("remote",None)
            reports.append(public)
            with gzip.GzipFile(filename=str(args.out/(r["id"]+"-scheduling.json.gz")),mode="wb",mtime=0) as f:f.write((json.dumps(t,separators=(",",":"))+"\n").encode())
    with gzip.GzipFile(filename=str(args.out/"trials.json.gz"),mode="wb",mtime=0) as f:f.write((json.dumps(reports,separators=(",",":"))+"\n").encode())
    (args.out/"summary.json").write_text(json.dumps(summaries,indent=2)+"\n",encoding="utf-8")
    print(json.dumps(dict(trials=len(reports),requests=sum(len(r["requests"] or []) for r in reports),MiB=sum(o["bytes"] for r in reports for o in r["requests"] or [])/2**20,allOK=all(r["ok"] for r in reports))))

if __name__=="__main__":main()
