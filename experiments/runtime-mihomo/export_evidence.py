"""Export explicit sanitized producer output, verify pinned source and hash Git text bytes."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import statistics as stats
import subprocess
import zipfile

ROOT=Path(__file__).resolve().parents[2]
def sha(data):return hashlib.sha256(data).hexdigest()
def read(path):return json.loads(path.read_text(encoding="utf-8"))
def write(path,data):path.write_text(json.dumps(data,indent=2,ensure_ascii=False)+"\n",encoding="utf-8",newline="\n")

def main():
    ap=argparse.ArgumentParser();ap.add_argument("--run",type=Path,required=True);ap.add_argument("--soak",type=Path,required=True);ap.add_argument("--history",type=Path,nargs="*",default=[]);ap.add_argument("--go",required=True)
    args=ap.parse_args();dest=ROOT/"docs/evidence/runtime-mihomo-20261013";dest.mkdir(parents=True,exist_ok=True)
    result=read(args.run/"results.json");soak=read(args.soak/"results.json")
    # Producer exports no raw input, URL or error text. Check before publication.
    serialized=json.dumps([result,soak]+[read(p/"results.json") for p in args.history])
    if re.search(r'://|(?i:authorization|privateResourceURL|ciphertext|password)|[A-Za-z]:\\\\',serialized):
        raise ValueError("privacy-screen-refused")
    write(dest/"results.json",result);write(dest/"soak.json",soak)
    summary={"date":"2026-10-13","baseRevision":result["baseRevision"],"checks":{"total":len(result["checks"]),"passed":sum(x["passed"] for x in result["checks"])},"soakPassed":all(x["passed"] for x in soak["runs"]),"comparisons":{},"real":{},"recommendation":"C for next production implementation; B finite feasibility demonstrated; no production switch"}
    for mode in ("B","C"):
        rows=[x for x in result["benchmarks"] if x["mode"]==mode]
        summary["comparisons"][mode]={"throughputMiBps":[128/x["bulkWallSeconds"] for x in rows],"throughputMedianMiBps":stats.median(128/x["bulkWallSeconds"] for x in rows),"idleWorkingSetMedianBytes":stats.median(sum(s["workingSetBytes"] for s in x["idle"]) for x in rows),"cpuSecondsPer128MiBMedian":stats.median(x["cpuSeconds"] for x in rows),"readyMedianMS":stats.median(x["startupMS"] for x in rows)}
        rows=[x for x in result["realBenchmarks"] if x["mode"]==mode]
        cold=[r["firstByteMS"] for x in rows for r in x["cold"]];warm=[r["firstByteMS"] for x in rows for r in x["warm"]]
        summary["real"][mode]={"samplesCold":len(cold),"samplesWarm":len(warm),"coldMedianMS":stats.median(cold),"coldRangeMS":[min(cold),max(cold)],"warmMedianMS":stats.median(warm),"warmRangeMS":[min(warm),max(warm)]}
    write(dest/"summary.json",summary)
    history=[]
    for i,p in enumerate(args.history):
        old=read(p/"results.json")
        history.append({"iteration":i+1,"binaries":old["binaries"],"checksTotal":len(old["checks"]),"failedChecks":[x for x in old["checks"] if not x["passed"]],"cleanup":old["cleanup"],"interpretation":("32-slot waiting implementation accumulated detached HTTP dials; sandbox socket table was empty, so no-listener observations were inconclusive. Later runner requires a positive control." if i==0 else "Fail-fast removed queue accumulation; 40 concurrent healthy requests exceeded experimental 32-operation cap. Final cap is 64 and retains the same 40-request test.")})
    write(dest/"history.json",history)
    identities={}
    for name in ("B.exe","C.exe","core.exe","fixture.exe"):
        raw=subprocess.check_output([args.go,"version","-m",str(args.run.parent/name)],text=True)
        identities[name]={"go":"go1.25.0","sha256":sha((args.run.parent/name).read_bytes()),"metadata":[line.strip() for line in raw.splitlines()[1:]]}
    if any(identities[n]["sha256"]!=result["binaries"][n]["sha256"] for n in identities):raise ValueError("binaries-changed")
    modulecache=Path(subprocess.check_output([args.go,"env","GOMODCACHE"],text=True).strip())
    prefix="github.com/metacubex/mihomo@v1.19.32/"
    archive=modulecache/"cache/download/github.com/metacubex/mihomo/@v/v1.19.32.zip"
    matched=0
    with zipfile.ZipFile(archive) as z:
        for n in z.namelist():
            if n.endswith("/"):continue
            if not n.startswith(prefix):raise ValueError("module-zip-prefix")
            if z.read(n)!=(modulecache/n).read_bytes():raise ValueError("module-source-mismatch")
            matched+=1
    # Compare shared modules by the actual linked build metadata, not just go.mod.
    def versions(binary):
        out={}
        for line in identities[binary]["metadata"]:
            parts=line.split()
            if parts and parts[0] in ("dep","mod") and len(parts)>2 and parts[2]!="(devel)":out[parts[1]]=parts[2]
        return out
    bv,cv=versions("B.exe"),versions("core.exe")
    shared=sorted(bv.keys()&cv.keys());mismatch=[n for n in shared if bv[n]!=cv[n]]
    if mismatch:raise ValueError("linked-shared-dependency-mismatch")
    write(dest/"build-identity.json",{"binaries":identities,"mihomoSourceFilesMatchingModuleZip":matched,"moduleZipSHA256":sha(archive.read_bytes()),"sharedLinkedModuleVersionsMatched":len(shared),"sharedLinkedModuleVersionMismatches":mismatch,"goModVerify":"not-passed: local replaced still-wanna-dance v0.0.0 missing ziphash; not a claim of all-module cache verification"})
    files=[p for p in dest.iterdir() if p.is_file() and p.name!="manifest.json"]
    source=[p for p in (ROOT/"experiments/runtime-mihomo").rglob("*") if p.is_file() and "__pycache__" not in p.parts]
    source += [ROOT/"experiments/managed-mihomo/run.py",ROOT/"docs/validation/mihomo-runtime-evaluation-20261013.md"]
    def entry(p):
        data=p.read_text(encoding="utf-8").replace("\r\n","\n").encode()
        return {"path":p.relative_to(ROOT).as_posix(),"bytes":len(data),"sha256":sha(data)}
    manifest={"kind":"development-evaluation","date":"2026-10-13","baseRevision":result["baseRevision"],"hashBasis":"git-blob (UTF-8 LF per .gitattributes)","binaryHashBasis":"actual-file-bytes","evidence":[entry(p) for p in sorted(files)],"source":[entry(p) for p in sorted(source)],"redaction":"Only counts, preset error categories, relative durations and build identities. No subscription, node configuration, signed URL, credentials or absolute personal paths. Private producer artifacts remain ignored."}
    write(dest/"manifest.json",manifest)
    print(json.dumps({"checks":summary["checks"],"soakPassed":summary["soakPassed"],"sourceFilesVerified":matched,"sharedModuleVersions":len(shared),"evidenceFiles":len(files)}))

if __name__=="__main__":main()
