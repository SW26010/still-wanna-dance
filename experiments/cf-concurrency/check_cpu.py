"""Content/plan validation and sanitized experiment provenance."""
import collections
import gzip
import hashlib
import json
from pathlib import Path
import sys
from run_cpu import plan

def main(root,out):
    expected={a["id"]:a for row in plan() for a in row}
    files=list(root.glob("r*-*.json"))
    assert {f.stem for f in files}==set(expected),"incomplete batch"
    hashes=json.loads((root/"reference.json").read_text())
    checks=[];attributes=collections.defaultdict(collections.Counter)
    for f in sorted(files):
        r=json.loads(f.read_text());a=expected[r["id"]]
        assert r["ok"] and r["kind"]==a["kind"] and r["target"]==a["n"]
        assert r["ip"]=="104.26.4.28" and r["host"]=="play.udon.dance" and r["song"]==10552
        assert r["logicalCPUs"]==16 and len(r["coreMasks"])==8 and sum(r["coreMasks"])==65535
        assert len(r["stages"])==len(r["planned"])
        assert all(abs(x["atMS"]-y["atMS"])<100 and x["n"]==y["n"] for x,y in zip(r["stages"],r["planned"]))
        assert abs(r["stopMS"]-(r["planned"][-1]["atMS"]+6000))<100
        assert r["peak"]==(0 if r["kind"]=="idle" else r["target"])
        for o in r["requests"] or []:
            assert not o.get("error") and o["status"]==206 and o["bytes"]==o["length"]==1048576
            assert o["offset"]%1048576==0 and 0<=o["offset"]<96*1048576
            assert o["hashes"]==[hashes[o["offset"]//1048576]] and o["targetIP"]=="104.26.4.28"
            assert o["remote"].rsplit(":",1)[0]=="104.26.4.28"
            for key in ("protocol","cache","colo","server"):attributes[key][o[key]]+=1
        checks.append(dict(id=r["id"],requests=len(r["requests"] or []),bytes=sum(o["bytes"] for o in r["requests"] or []),peak=r["peak"],contentVerified=True,scheduleVerified=True))
    preliminary=[]
    for f in sorted((root/"preliminary-cap01").glob("r1-*.json")):
        r=json.loads(f.read_text());r.pop("pid")
        for o in r["requests"] or []:o.pop("remote",None)
        preliminary.append(r)
    with gzip.GzipFile(filename=str(out/"preliminary.json.gz"),mode="wb",mtime=0) as f:f.write((json.dumps(preliminary,separators=(",",":"))+"\n").encode())
    total=sum(c["bytes"] for c in checks)
    pre_bytes=sum(o["bytes"] for r in preliminary for o in r["requests"] or [])
    interference=[]
    for f in sorted((root/"interference-r2").glob("r2-*.json")):
        r=json.loads(f.read_text());r.pop("pid")
        for o in r["requests"] or []:o.pop("remote",None)
        interference.append(r)
    with gzip.GzipFile(filename=str(out/"interference.json.gz"),mode="wb",mtime=0) as f:f.write((json.dumps(interference,separators=(",",":"))+"\n").encode())
    interference_bytes=sum(o["bytes"] for r in interference for o in r["requests"] or [])
    assert total<20*1024**3 and total+pre_bytes+interference_bytes+(2048+205)*1048576<24*1024**3
    result=dict(allPassed=True,trials=checks,totalBytes=total,preliminaryBytes=pre_bytes,attributes=attributes,primaryExcludedTrials=[],preliminaryExclusion="Entire incomplete 459bcfe block excluded: local 1024-block traffic cap at staircase 32-worker transition; all data retained, no speed-based exclusion.",tests=["Go Windows race experiment package passed before and after primary bound correction","go vet experiment package passed","Eight Python analysis tests passed"],sourceCommit="d6f12801e8fea951019c3dd16e558faa22f12744",exeSHA256=hashlib.sha256((root/"cpu-probe.exe").read_bytes()).hexdigest())
    result["interferenceCompletedBytes"]=interference_bytes
    result["interferenceInterruptedTrialUpperBytes"]=2048*1048576
    result["interferenceExclusion"]="First attempt of round 2 overlapped ETW CSV analysis from round 1 at its beginning. Entire incomplete attempt excluded by interference criterion, then repeated in the same predefined order. Eight finished trials retained; current unsaved trial was terminated. Not excluded by throughput."
    (out/"checks.json").write_text(json.dumps(result,indent=2)+"\n")
    env=json.loads((root/"environment.json").read_text(encoding="utf-8-sig"));env["dnsIPv4"]=["104.26.4.28","104.26.5.28","172.67.69.58"];env["coreMasks"]=[3,12,48,192,768,3072,12288,49152]
    (out/"environment.json").write_text(json.dumps(env,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
    (out/"plan.json").write_text((root/"plan.json").read_text(),encoding="utf-8")
    (out/"reference.json").write_text(json.dumps(hashes,indent=2)+"\n")
    print(json.dumps(dict(trials=len(checks),requests=sum(c["requests"] for c in checks),MiB=total/1048576,preliminaryMiB=pre_bytes/1048576,attributes=attributes)))

if __name__=="__main__":main(Path(sys.argv[1]),Path(sys.argv[2]))
