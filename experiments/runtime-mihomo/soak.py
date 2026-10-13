"""Bounded local continuous-load supplement; uses the exact already built B/C binaries."""
import argparse
import json
from pathlib import Path
import secrets
import subprocess
import sys
import time

sys.dont_write_bytecode=True
import run as r
m=r.m

def main():
    ap=argparse.ArgumentParser();ap.add_argument("--bin",type=Path,required=True);ap.add_argument("--seconds",type=int,default=60)
    args=ap.parse_args()
    if not 10<=args.seconds<=120:raise ValueError("duration must be 10..120 seconds per mode")
    root=args.bin/("soak-"+secrets.token_hex(5));m.private_dir(root)
    out={"secondsPerMode":args.seconds,"binaries":{n:m.sha(args.bin/n) for n in ("B.exe","C.exe","core.exe")},"runs":[]}
    dns=m.DNSServer(("127.0.0.1",0),m.DNSFixture);dns.queries=[];m.background(dns)
    dnsaddr=f"127.0.0.1:{dns.server_address[1]}"
    fj=m.Job();manifest=root/"fixture.private.json"
    fp=fj.start([str(args.bin/"fixture.exe"),str(manifest)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,env=m.clean_env())
    p=c=None
    try:
        if not m.waitfor(manifest.exists):raise RuntimeError("fixture-timeout")
        node=json.loads(manifest.read_text())["nodes"][0];payload=json.dumps({"proxies":[node]})
        for mode in ("B","C"):
            if mode=="B":
                home=root/"b";home.mkdir()
                p=r.Probe(args.bin/"B.exe",{"Mode":"B","Payload":payload,"DNS":dnsaddr,"Home":str(home)})
            else:
                c=m.Core(args.bin/"core.exe",root,[payload.encode()],1,dnsaddr,mutate=lambda cfg:cfg.update({"log-level":"silent","find-process-mode":"off"}))
                if not c.valid():raise RuntimeError("invalid-C-mapping")
                p=r.Probe(args.bin/"C.exe",{"Mode":"C","Endpoints":c.endpoints()})
            if not p.ready.get("ready"):raise RuntimeError("probe-not-ready")
            row={"mode":mode,"samples":[],"requests":0,"failures":0};start=time.perf_counter();batch=0
            while time.perf_counter()-start<args.seconds:
                rs=p.request(Iterations=8,Concurrent=True);row["requests"]+=len(rs);row["failures"]+=sum(not x["ok"] for x in rs)
                # Exercise a canceled resource wait each second, then healthy reuse.
                rs=p.request("http://business.invalid/stall",TimeoutMS=50)
                row["failures"]+=sum(x["ok"] or x["elapsedMS"]>1000 for x in rs)
                if batch%10==0:
                    row["samples"].append({"elapsedSeconds":time.perf_counter()-start,"state":p.ask({"Stats":True}),"memory":sum(m.stats(x)["workingSetBytes"] for x in [p.p]+([c.p] if c else []))})
                    print(mode,"soak",batch,"seconds",flush=True)
                batch+=1;time.sleep(max(0,start+batch-time.perf_counter()))
            row["durationSeconds"]=time.perf_counter()-start
            row["retired"]=p.ask({"Retire":True,"URL":"http://business.invalid/api"})["retired"]
            row["afterRetire"]=p.ask({"Stats":True})
            p.close();p=None
            if c:c.close();c=None
            row["passed"]=row["requests"]>=args.seconds*8 and row["failures"]==0 and row["retired"] and (mode!="B" or row["afterRetire"]["pending"]==0)
            out["runs"].append(row)
    except BaseException as exc:out["fatal"]=type(exc).__name__
    finally:
        if p:p.close()
        if c:c.close()
        fj.close();fp.wait(timeout=5);dns.shutdown();dns.server_close()
        out["providerPlaintextRemaining"]=len(list(root.rglob("p*.txt")))
        (root/"results.json").write_text(json.dumps(out,indent=2)+"\n",encoding="utf-8")
        print("RESULT",root.relative_to(r.ROOT).as_posix(),flush=True)
    return int(bool(out.get("fatal")) or len(out["runs"])!=2 or not all(x["passed"] for x in out["runs"]))

if __name__=="__main__":sys.exit(main())
