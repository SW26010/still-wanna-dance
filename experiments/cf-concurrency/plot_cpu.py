import gzip
import json
from pathlib import Path
import statistics
import sys
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

matplotlib.rcParams.update({"svg.hashsalt":"swd-cf-evidence-v1", "svg.fonttype":"none"})

def prepare(root):
    """Derive small plotting inputs from the verified complete archive."""
    rows=json.loads((root/"summary.json").read_text())
    trials=json.loads(gzip.decompress((root/"trials.json.gz").read_bytes()))
    result={"schemaVersion":1,"rows":[],"timeline":{}}
    keys=("mibPerSec","machinePercent","cpuSecondsPerGiB","peak100msCores")
    for r in rows:
        result["rows"].append({"id":r["id"],"kind":r["kind"],"target":r["target"],"windows":{k:{key:v[key] for key in keys} for k,v in r["windows"].items() if k.startswith("steady")}})
    for kind in ("burst","stagger","stepped"):
        r=next(r for r in rows if r["id"]==f"r2-{kind}-32")
        raw=next(x for x in trials if x["id"]==r["id"])
        series=r["windows"]["active"]["series100msCores"]
        ts=[(i+.5)*.1 for i in range(len(series))]
        result["timeline"][kind]={"cpu":series,"activeHTTP":[sum(o["startMS"]<=t*1000<o["startMS"]+o["elapsedMS"] for o in raw["requests"]) for t in ts],"stageMS":[s["atMS"] for s in raw["stages"]]}
    (root/"plot-data.json").write_text(json.dumps(result,indent=2)+"\n",encoding="utf-8")

def draw(root):
    inputs=json.loads((root/"plot-data.json").read_text())
    data=inputs["rows"]
    levels=[1,4,8,12,16,24,32]
    colors={"burst":"#2764ae","stagger":"#da6c22","stepped":"#279264"}
    def values(kind,n,key):
        name=f"steady-{n}" if kind=="stepped" else "steady"
        return [r["windows"][name][key] for r in data if r["kind"]==kind and (kind=="stepped" or r["target"]==n)]
    fig,axs=plt.subplots(2,2,figsize=(12,8),layout="constrained")
    for ax,key,title,ylabel,scale in [(axs[0,0],"mibPerSec","Sustained useful transfer","MiB / s",1),(axs[0,1],"machinePercent","Process CPU (16 logical processors)","Total machine %",1),(axs[1,0],"cpuSecondsPerGiB","CPU cost per transferred GiB","CPU seconds / GiB",1),(axs[1,1],"peak100msCores","Peak complete 100 ms window","Total machine %",100/16)]:
        for kind in colors:
            ns=levels[1:] if kind=="stagger" else levels
            allvalues=[[v*scale for v in values(kind,n,key)] for n in ns]
            mid=[statistics.median(v) for v in allvalues]
            ax.errorbar(ns,mid,yerr=[[m-min(v) for m,v in zip(mid,allvalues)],[max(v)-m for m,v in zip(mid,allvalues)]],marker="o",capsize=3,label=kind,color=colors[kind])
        ax.set(title=title,xlabel="HTTP worker limit",ylabel=ylabel,xticks=levels);ax.grid(alpha=.2);ax.set_ylim(bottom=0)
    axs[0,0].legend();fig.suptitle("CF single-IP steady phase | three repetitions | median and observed range",fontsize=14)
    fig.savefig(root/"cpu-summary.svg",metadata={"Date":None});plt.close(fig)
    fig,axs=plt.subplots(3,1,figsize=(12,9),layout="constrained")
    for ax,kind in zip(axs,colors):
        timeline=inputs["timeline"][kind]
        series=timeline["cpu"]
        ts=[(i+.5)*.1 for i in range(len(series))]
        ax.plot(ts,series,color=colors[kind],lw=1.3,label="CPU: 100 ms average")
        second=ax.twinx()
        active=timeline["activeHTTP"]
        second.plot(ts,active,color="#858585",lw=1,alpha=.65,label="Active HTTP requests");second.set_ylim(0,35);second.set_ylabel("Active HTTP requests")
        for at in timeline["stageMS"]:ax.axvline(at/1000,color="black",alpha=.07)
        ax.set(title=f"{kind} to 32 | predetermined repetition 2",xlabel="Seconds since launch",ylabel="Logical-core equivalents",ylim=(0,max(.5,max(series)*1.12)));ax.grid(alpha=.2)
        ax.legend(loc="upper left");second.legend(loc="upper right")
    fig.suptitle("CPU work is intermittent even when many HTTP requests are active",fontsize=14)
    fig.savefig(root/"cpu-timeline.svg",metadata={"Date":None});plt.close(fig)

if __name__=="__main__":
    root=Path(sys.argv[1])
    if "--prepare" in sys.argv:prepare(root)
    else:draw(root)
