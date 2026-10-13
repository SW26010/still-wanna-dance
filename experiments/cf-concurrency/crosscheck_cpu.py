"""Independent xperf CPU totals for the exact same process/time window."""
import gzip
import json
from pathlib import Path
import re
import subprocess
import sys

def main(root,out):
    summaries=json.loads((out/"summary.json").read_text())
    xperf=r"C:\Program Files (x86)\Windows Kits\10\Windows Performance Toolkit\xperf.exe"
    checks=[]
    for r in summaries:
        # Cover every independent mode, each repetition, and all burst levels.
        if not (r["kind"] in ("burst","idle","stepped") or r["target"]==32):continue
        source=json.loads((root/(r["id"]+".json")).read_text())
        trace=json.loads(gzip.decompress((out/(r["id"]+"-scheduling.json.gz")).read_bytes()))
        round_no=int(r["id"][1]);stem="cpu-repeat-r2" if round_no==2 else f"cpu-main-r{round_no}"
        output=root/(r["id"]+"-xperf-private.txt")
        with (root/"crosscheck-private.log").open("a") as log:
            subprocess.run([xperf,"-i",str(root/(stem+".etl")),"-o",str(output),"-a","cswitch","-process","-exc_dpcisr","-range",str(trace["traceBeginUS"]),str(trace["traceBeginUS"]+trace["durationUS"])],stdout=log,stderr=log,check=True)
        match=re.search(r"^\s*(\d+),\s*cpu-probe\.exe\s*\(\s*"+str(source["pid"])+r"\)",output.read_text(errors="replace"),re.M)
        assert match, "missing independent process utilization"
        reference=int(match[1]);actual=round(r["windows"]["full"]["cpuMS"]*1000)
        difference=abs(reference-actual)
        assert difference<=max(500,reference*.005),f"CPU discrepancy for {r['id']}: {reference} vs {actual}"
        checks.append(dict(id=r["id"],xperfCPUUS=reference,parsedCPUUS=actual,absoluteDifferenceUS=difference))
        print(f"crosschecked {r['id']}: delta_us={difference}",flush=True)
    (out/"crosschecks.json").write_text(json.dumps(dict(allPassed=True,method="xperf cswitch -process -exc_dpcisr; exact ETW begin/end markers; tolerance max(500 us, 0.5%)",checks=checks),indent=2)+"\n")

if __name__=="__main__":main(Path(sys.argv[1]),Path(sys.argv[2]))
