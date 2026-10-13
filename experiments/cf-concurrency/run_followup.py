"""Bounded local TLS/complete-cache experiments; no product defaults are changed."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import random
import ssl
import subprocess
import time
import urllib.request

REPO = Path(__file__).resolve().parents[2]
GO = Path(r"C:\Users\Himalia\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.25.0.windows-amd64\bin\go.exe")
ROOT = REPO / "artifacts/cf-cpu-followup"
LEVELS = (4, 8, 16, 32)

def save(path, value):
    path.write_text(json.dumps(value, indent=2)+"\n", encoding="utf-8")

def trace(action, stem="", expect=None):
    ident = f"{action}-{time.monotonic_ns()}"
    temp = ROOT / "trace-request.tmp"
    save(temp, dict(id=ident, action=action, stem=stem))
    temp.replace(ROOT / "trace-request.json")
    expected = expect or dict(start="recording", stop="stopped", quit="exited")[action]
    deadline = time.monotonic()+55
    while time.monotonic()<deadline:
        status=json.loads((ROOT/"trace-status.json").read_text(encoding="utf-8-sig"))
        if status["id"]==ident:
            if status["state"]==expected:return status
            if status["state"] in ("error","exited"):raise RuntimeError(status["detail"])
        time.sleep(.2)
    raise RuntimeError("trace acknowledgement timeout")

def build(race=False):
    env=os.environ.copy();env["GOTOOLCHAIN"]="local";env["CGO_ENABLED"]="1" if race else "0"
    if race:env["CC"]=str(REPO/"artifacts/issue14-windows-final/toolchain/w64devkit/bin/gcc.exe")
    results=[]
    for n in ((4,32) if race else LEVELS):
        original=REPO/"internal/cacheproxy/range_work.go"
        overlay_file=ROOT/f"range-work-{n}.go"
        source=original.read_text().replace("const rangeBackgroundWorkers = 3",f"const rangeBackgroundWorkers = {n-1}")
        assert source.count(f"const rangeBackgroundWorkers = {n-1}")==1
        overlay_file.write_text(source,encoding="utf-8")
        overlay=ROOT/f"overlay-{n}.json";save(overlay,{"Replace":{str(original):str(overlay_file)}})
        exe=ROOT/(f"follow-{n}"+("-race" if race else "")+".exe")
        command=[str(GO),"test","-c","-tags=no_easytier","-overlay",str(overlay),"-o",str(exe),"./internal/cacheproxy"]
        if race:command.insert(2,"-race")
        subprocess.run(command,cwd=REPO,env=env,check=True)
        results.append(dict(n=n,workers=n-1,exe=exe.name,sha256=hashlib.sha256(exe.read_bytes()).hexdigest(),overlaySHA256=hashlib.sha256(overlay_file.read_bytes()).hexdigest()))
        print(f"built {exe.name}",flush=True)
    save(ROOT/("race-builds.json" if race else "builds.json"),results)

def request_origin(info,path):
    context=ssl.create_default_context(cadata=info["Certificate"])
    with urllib.request.urlopen("https://"+info["Address"]+path, context=context, timeout=5) as response:
        data=response.read()
        return json.loads(data) if data else None

def origin_start(stem):
    info_path=ROOT/(stem+"-origin.json")
    if info_path.exists():raise RuntimeError("origin evidence already exists")
    config=ROOT/(stem+"-origin-plan.json");save(config,dict(Mode="origin",RateMiB=16,Out=str(info_path)))
    env=os.environ.copy();env["SWD_CPU_FOLLOWUP_PLAN"]=str(config)
    log=(ROOT/(stem+"-origin-private.log")).open("w")
    process=subprocess.Popen([str(ROOT/"follow-4.exe"),"-test.run=^TestCPUFollowup$","-test.timeout=46m"],env=env,stdout=log,stderr=subprocess.STDOUT,cwd=REPO)
    deadline=time.monotonic()+20
    while not info_path.exists():
        if process.poll() is not None or time.monotonic()>deadline:raise RuntimeError("origin failed to start")
        time.sleep(.1)
    info=json.loads(info_path.read_text());return process,log,info_path,info

def arm(ident,mode,n=4,scenario="",warm=False,blocks=None):
    return dict(ID=ident,Mode=mode,N=n,Scenario=scenario,Warm=warm,Blocks=blocks or (96 if mode=="raw" else 64))

def plan():
    rng=random.Random(2026101402);rounds=[]
    for repetition in range(1,6):
        arms=[]
        # Five paired repetitions; longer than the earlier three-repetition CPU study.
        for n in LEVELS:
            for warm in (False,True):arms.append(arm(f"r{repetition}-raw-{n}-{'warm' if warm else 'cold'}","raw",n,warm=warm))
            for scenario in ("prefetch","compete","cancel"):
                arms.append(arm(f"r{repetition}-engine-{n}-{scenario}","engine",n,scenario))
        rng.shuffle(arms)
        rounds.append([arm(f"r{repetition}-idle","idle",1)]+arms)
    return dict(seed=2026101402,rateMiB=16,maxConcurrency=32,rawBlocks=96,engineBlocks=64,rounds=rounds,
                engineBudgets={"4":[6,4,3],"8":[8,6,7],"16":[16,14,15],"32":[32,30,31]},
                budgetMeaning=["total requests","background requests","background workers per video"],
                localPayloadBudgetMiB=12288,publicNetworkBytes=0)

def trial(item,origin_path,race=False):
    # Bound the complete local batch, including warmups. Reserve 1 GiB for
    # incomplete pilots/race runs, then at most another 512 MiB for this trial.
    used=0
    for existing in ROOT.glob("r*-*.json"):
        value=json.loads(existing.read_text())
        if "OriginStats" in value:
            used+=value["OriginStats"]["bytes"]
            if value["plan"]["Warm"]:used+=value["target"]*1048576
    if used+1536*1048576>12288*1048576:
        raise RuntimeError("local transfer budget exhausted")
    p=dict(item,OriginInfo=str(origin_path),Out=str(ROOT/(item["ID"]+".json")),Storage=str(ROOT/(item["ID"]+"-cache")))
    if Path(p["Out"]).exists():raise RuntimeError("refusing to replace trial")
    path=ROOT/(item["ID"]+"-plan.json");save(path,p)
    env=os.environ.copy();env["SWD_CPU_FOLLOWUP_PLAN"]=str(path)
    n=4 if item["Mode"] in ("raw","idle") else item["N"]
    exe=ROOT/(f"follow-{n}"+("-race" if race else "")+".exe")
    with (ROOT/(item["ID"]+"-private.log")).open("w") as log:
        result=subprocess.run([str(exe),"-test.run=^TestCPUFollowup$","-test.timeout=70s","-test.v"],env=env,cwd=REPO,stdout=log,stderr=subprocess.STDOUT,timeout=80)
    if result.returncode:raise RuntimeError("trial failed: "+item["ID"])
    data=json.loads(Path(p["Out"]).read_text())
    if not data["ok"]:raise RuntimeError("invalid trial: "+item["ID"])
    print(f'{item["ID"]}: {data["endMS"]:.1f} ms, peak={data["PeakRequests"]}, tls={data["TLSHandshakes"]}',flush=True)
    return data

def run_batch(stem,items,race=False,etw=True):
    process,log,origin_path,info=origin_start(stem);started=False;reports=[]
    try:
        if etw:trace("start",stem);started=True
        for item in items:
            reports.append(trial(item,origin_path,race))
            time.sleep(.3)
    finally:
        if started:trace("stop")
        try:request_origin(info,"/quit")
        finally:
            try:process.wait(timeout=10)
            except subprocess.TimeoutExpired:process.terminate();process.wait(timeout=10)
            log.close()
    save(ROOT/(stem+"-receipt.json"),dict(stem=stem,ids=[r["id"] for r in reports],originExited=process.returncode==0))

def main():
    p=argparse.ArgumentParser();p.add_argument("action",choices=("build","build-race","helper-check","pilot","race","round","quit"));p.add_argument("--round",type=int,choices=range(1,6));args=p.parse_args();ROOT.mkdir(exist_ok=True)
    if args.action in ("build","build-race"):build(args.action=="build-race")
    elif args.action=="helper-check":
        result=trace("invalid",expect="error");trace("start","helper-smoke");time.sleep(.5);trace("stop");save(ROOT/"helper-check.json",dict(recoverableError=result["state"]=="error",startStopPassed=True))
    elif args.action=="pilot":run_batch("pilot",[arm("pilot-raw","raw",32,blocks=16),arm("pilot-engine-prefetch","engine",32,"prefetch",blocks=32),arm("pilot-engine-compete","engine",32,"compete",blocks=32),arm("pilot-engine-cancel","engine",32,"cancel",blocks=32)])
    elif args.action=="race":run_batch("race",[arm("race-raw","raw",4,blocks=8),arm("race-engine-compete","engine",32,"compete",blocks=32),arm("race-engine-cancel","engine",32,"cancel",blocks=32)],race=True,etw=False)
    elif args.action=="round":
        if not args.round:p.error("round number required")
        design=plan();save(ROOT/"plan.json",design);run_batch(f"follow-r{args.round}",design["rounds"][args.round-1])
    else:trace("quit")

if __name__=="__main__":main()
