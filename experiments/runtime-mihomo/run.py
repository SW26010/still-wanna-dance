"""Finite B/C experiment. All private input stays in pipes or ACL-restricted artifacts.

Reuses the C experiment's Windows ownership, network fixtures and measurement
code; never changes hosts, product settings, system proxies or user media.
"""
import argparse
import base64
import hashlib
import http.server
import importlib.util
import json
from pathlib import Path
import queue
import secrets
import socket
import subprocess
import sys
import threading
import time

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("managed", ROOT / "experiments/managed-mihomo/run.py")
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class Probe:
    def __init__(self, binary, init):
        self.job = m.Job()
        self.p = self.job.start([str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.DEVNULL, text=True, encoding="utf-8", env=m.clean_env())
        self.lines = queue.Queue()
        def read():
            for line in self.p.stdout: self.lines.put(line)
            self.lines.put(None)
        threading.Thread(target=read, daemon=True).start()
        self.start = time.perf_counter()
        try:
            self.ready = self.ask(init)
            self.start_ms = (time.perf_counter()-self.start)*1000
        except BaseException:
            self.close(); raise
    def ask(self, cmd, timeout=65):
        self.p.stdin.write(json.dumps(cmd)+"\n"); self.p.stdin.flush()
        line = self.lines.get(timeout=timeout)
        if line is None: raise RuntimeError("probe-ended-without-response")
        try: return json.loads(line)
        except ValueError: raise RuntimeError("non-json-probe-output") from None
    def request(self, url="http://business.invalid/api", **kwargs):
        return self.ask({"URL":url, **kwargs})["observations"]
    def close(self):
        if self.p.poll() is None:
            try:
                self.p.stdin.close(); self.p.wait(timeout=10)
            except (OSError, subprocess.TimeoutExpired): pass
        self.job.close(); self.p.wait(timeout=5)

class Fixture(m.HTTPFixture):
    def do_GET(self):
        if self.path == "/range":
            self.server.hits += 1
            data = bytes(range(256))*16
            if self.headers.get("Range") != "bytes=100-999":
                self.send_error(400); return
            self.send_response(206); self.send_header("Content-Range", "bytes 100-999/4096")
            self.send_header("Content-Length", "900"); self.end_headers(); self.wfile.write(data[100:1000]); return
        super().do_GET()

class Stall(m.socketserver.BaseRequestHandler):
    def handle(self):
        with self.server.lock: self.server.active += 1; self.server.peak=max(self.server.peak,self.server.active)
        try:
            self.request.settimeout(8)
            while self.request.recv(4096): pass
        except OSError: pass
        finally:
            with self.server.lock: self.server.active -= 1

def main():
    ap=argparse.ArgumentParser();ap.add_argument("--bin",type=Path,required=True);ap.add_argument("--private-subscription",type=Path)
    args=ap.parse_args(); run=args.bin/("run-"+secrets.token_hex(5));m.private_dir(run)
    out={"date":"2026-10-13","baseRevision":subprocess.check_output(["git","rev-parse","HEAD"],cwd=ROOT,text=True).strip(),
         "scope":"isolated development PoC; no product backend switch or live acceptance", "checks":[],"benchmarks":[],"private":[],
         "binaries":{name:{"sha256":m.sha(args.bin/name),"bytes":(args.bin/name).stat().st_size} for name in ("B.exe","C.exe","core.exe","fixture.exe")},"realBenchmarks":[]}
    probes=[];cores=[];servers=[];fp=None;fj=None
    hosts=Path(m.os.environ["SystemRoot"])/"System32/drivers/etc/hosts"
    before=m.sha(hosts)
    def record(name, passed, **data):
        out["checks"].append({"name":name,"passed":bool(passed),**data})
        print(name, "PASS" if passed else "FAIL", flush=True)
    def prepare(payload):
        p=Probe(args.bin/"B.exe",{"Prepare":True,"Payload":payload}); probes.append(p)
        result=p.ready;p.close();probes.remove(p);return result
    def start(mode,payload,dns):
        checked=prepare(payload)
        if not checked.get("accepted"): raise RuntimeError("capability-gate-refused")
        # Native conversion plus application-owned names; no protocol schema rewrite.
        nodes=checked["nodes"]
        core=None
        if mode=="B":
            home=run/("b-"+secrets.token_hex(4));home.mkdir()
            p=Probe(args.bin/"B.exe",{"Mode":"B","Payload":payload,"DNS":dns,"Home":str(home)})
        else:
            core=m.Core(args.bin/"core.exe",run,[json.dumps({"proxies":[n]}).encode() for n in nodes],len(nodes),dns,
                        mutate=lambda c:c.update({"log-level":"silent","find-process-mode":"off"}))
            cores.append(core)
            if not core.valid(): raise RuntimeError("C-fixed-mapping-failed")
            p=Probe(args.bin/"C.exe",{"Mode":"C","Endpoints":core.endpoints()})
        probes.append(p)
        if not p.ready.get("ready"): raise RuntimeError("runtime-not-ready")
        return p,core
    def stop(p,c):
        p.close();probes.remove(p)
        if c: c.close();cores.remove(c)
    try:
        dns=m.background(m.DNSServer(("127.0.0.1",0),m.DNSFixture));dns.queries=[];servers.append(dns)
        dnsaddr=f"127.0.0.1:{dns.server_address[1]}"
        fixtures=[]
        for marker in ("A","B"):
            h=m.background(http.server.ThreadingHTTPServer(("127.0.0.1",0),Fixture));h.marker=marker;h.hits=0;h.streaming=threading.Event();servers.append(h)
            s=m.TCPServer(("127.0.0.1",0),m.SocksFixture);s.http=h;s.targets=[];s.fail=False;m.background(s);fixtures.append(s);servers.append(s)
        nodes=[json.loads(m.socknode("DIRECT",s.server_address[1],"proxy.invalid"))["proxies"][0] for s in fixtures]
        observable=m.socket_inventory(m.os.getpid())
        sentinel=any(x["LocalPort"]==fixtures[0].http.server_port for x in observable["tcp"])
        record("socket-inventory-positive-control",sentinel)
        if not sentinel: raise RuntimeError("socket-inventory-unavailable")
        payload=json.dumps({"proxies":nodes,"tun":{"enable":True},"listeners":[{"port":54321}],"rules":["MATCH,DIRECT"]})
        for name,change in [("chain",{"dialer-proxy":"DIRECT"}),("alias",{"DIALER_PROXY":"DIRECT"}),("interface",{"interface_name":"x"}),("route",{"routing-mark":1}),("file",{"private-key":"private-file"}),("nested-file",{"tls-opts":{"certificate":"file"}}),("dynamic",{"type":"select"}),("direct",{"type":"direct"}),("overlay",{"type":"tailscale"}),("plugin",{"plugin":"anything"}),("discovery",{"realm-opts":{}})]:
            bad={**nodes[0],**change}
            r=prepare(json.dumps({"proxies":[bad]}));record("gate-"+name,not r.get("accepted"),reason=r.get("reason"))
        record("gate-empty",not prepare("proxies: []").get("accepted"))
        record("gate-malformed",not prepare("proxies: [null]").get("accepted"))
        vmess={"v":"2","ps":"fixture","add":"127.0.0.1","port":"65534","id":"d342d11e-d424-4583-b36e-524ab1f0afa4","aid":"0","scy":"auto","net":"ws","type":"none","host":"fixture.invalid","path":"/socket","tls":""}
        uri="vmess://"+base64.b64encode(json.dumps(vmess).encode()).decode()
        converted=prepare(uri)
        formats={"yaml":json.dumps({"proxies":converted["nodes"]}),"uri":uri,"base64":base64.b64encode(uri.encode()).decode()}
        for fmt,raw in formats.items():
            for mode in ("B","C"):
                p,c=start(mode,raw,dnsaddr);record(mode+"-native-vmess-ws-"+fmt,p.ready.get("channels")==1);stop(p,c)
        for mode in ("B","C"):
            p,c=start(mode,payload,dnsaddr)
            inventory=m.socket_inventory(p.p.pid)
            record(mode+"-probe-no-listener",inventory["tcp"]==[],inventory=inventory)
            if c:
                ci=m.socket_inventory(c.p.pid);record("C-only-loopback-listeners",len(ci["tcp"])==3 and all(x["LocalAddress"]=="127.0.0.1" for x in ci["tcp"]),inventory=ci)
            result=p.request(Concurrent=True,Iterations=20)
            record(mode+"-concurrent-fixed-exits",len(result)==40 and all(r["ok"] and r["marker"]==("A" if r["index"]==0 else "B") for r in result))
            result=p.request("http://business.invalid/range",Range="bytes=100-999")
            expected=hashlib.sha256((bytes(range(256))*16)[100:1000]).hexdigest()
            record(mode+"-range",len(result)==2 and all(r["status"]==206 and r["bytes"]==900 and r["sha256"]==expected for r in result))
            hits=sum(s.http.hits for s in fixtures);result=p.request(PreCancel=True)
            record(mode+"-pre-cancel",all(not r["ok"] for r in result) and hits==sum(s.http.hits for s in fixtures))
            result=p.request("http://business.invalid/stall",TimeoutMS=150)
            record(mode+"-header-timeout",all(not r["ok"] and r["elapsedMS"]<1000 for r in result),requests=result)
            result=p.request("http://business.invalid/stream",CancelAfterMS=150)
            record(mode+"-body-cancel",all(not r["ok"] and r["elapsedMS"]<1000 for r in result),requests=result)
            record(mode+"-healthy-after-cancel",all(r["ok"] for r in p.request()))
            if mode=="B":
                state=p.ask({"Stats":True});record("B-hosts-disabled",state["systemHosts"] is False,state=state)
                record("B-standard-globals-unchanged",state["standardResolverUnchanged"] and state["standardTransportUnchanged"])
                p.ask({"ShutdownOnly":True});after=p.request()
                record("B-shutdown-alone-not-disposal",all(r["ok"] for r in after),state=p.ask({"Stats":True}))
            retired=p.ask({"Retire":True,"URL":"http://business.invalid/api"})
            record(mode+"-retired-transports",retired["retired"])
            if mode=="B":record("B-retired-drained",p.ask({"Stats":True})["pending"]==0,state=p.ask({"Stats":True}))
            stop(p,c)
            # Fresh failed proxy, target IP is reachable directly. Count its hits.
            fixtures[0].fail=True
            p,c=start(mode,json.dumps({"proxies":[nodes[0]]}),dnsaddr)
            target=f"http://127.0.0.1:{fixtures[0].http.server_port}/api";hits=fixtures[0].http.hits
            failed=p.request(target,TimeoutMS=500)
            record(mode+"-failed-proxy-no-direct",not failed[0]["ok"] and hits==fixtures[0].http.hits)
            stop(p,c);fixtures[0].fail=False
        record("destination-dns-stays-remote","proxy.invalid" in dns.queries and "business.invalid" not in dns.queries,proxyQueries=dns.queries.count("proxy.invalid"),businessQueries=dns.queries.count("business.invalid"))
        # Repeated stalled handshakes: observe both frontend deadline and eventual
        # core cleanup; B admits at most 64 complete Tunnel operations, no queue.
        stall=m.TCPServer(("127.0.0.1",0),Stall);stall.lock=threading.Lock();stall.active=stall.peak=0;m.background(stall);servers.append(stall)
        stalled=m.socknode("stall",stall.server_address[1]).decode()
        for mode in ("B","C"):
            p,c=start(mode,stalled,dnsaddr);samples=[];all_failed=True
            for _ in range(6):
                result=p.request(Iterations=32,Concurrent=True,TimeoutMS=100)
                all_failed=all_failed and len(result)==32 and all(not r["ok"] and r["elapsedMS"]<1000 for r in result)
                samples.append({"sockets":stall.active,"probe":p.ask({"Stats":True}),"memory":sum(m.stats(x)["workingSetBytes"] for x in [p.p]+([c.p] if c else []))})
            drained=m.waitfor(lambda:stall.active==0,seconds=7)
            state=p.ask({"Stats":True})
            record(mode+"-cancel-burst-converges",all_failed and drained and (mode!="B" or state["pending"]==0 and state["peakPending"]<=state["slotLimit"]),samples=samples,final=state)
            stop(p,c)
        # Synthetic panic only, not a claim of an upstream bug.
        p,c=start("B",payload,dnsaddr)
        p.p.stdin.write('{"Panic":true}\n');p.p.stdin.flush();p.p.wait(timeout=5)
        record("B-background-panic-terminates-host",p.p.returncode!=0);stop(p,c)
        p,c=start("C",payload,dnsaddr);c.job.close();c.p.wait(timeout=5)
        failed=p.request(TimeoutMS=500)
        record("C-core-fault-host-survives",p.p.poll() is None and all(not r["ok"] for r in failed));stop(p,c)
        # Optional SAME source and SAME first two nodes, both modes. No URL fetch.
        if args.private_subscription:
            raw=args.private_subscription.read_text(encoding="utf-8").strip();checked=prepare(raw)
            count=len(checked.get("nodes",[]));record("private-native-prepare",checked.get("accepted") and count>0,count=count)
            for mode in ("B","C"):
                p,c=start(mode,raw,"1.1.1.1");record(mode+"-private-full-load",p.ready.get("channels")==count,count=count);stop(p,c)
            selected=json.dumps({"proxies":checked["nodes"][:2]})
            shared_url=""
            for mode in ("B","C"):
                p,c=start(mode,selected,"1.1.1.1")
                result=p.ask({"Monitor":True,"ExportSource":True})
                source=result.pop("privateResourceURL","")
                if not shared_url:shared_url=source
                resource=[r for r in result.get("results",[]) if r["operation"]=="resource"]
                # Save failure as failure; network variability is not masked.
                record(mode+"-private-monitor-business",result.get("completed") and result.get("businessStatus")==302 and any(r["state"]=="available" and r["bytes"]>0 for r in resource),observation=result)
                stop(p,c)
            if shared_url:
                for round_index in range(3):
                    for mode in (("B","C") if round_index%2==0 else ("C","B")):
                        p,c=start(mode,selected,"1.1.1.1");processes=[p.p]+([c.p] if c else [])
                        initial=[m.stats(x) for x in processes]
                        cold=p.request(shared_url,Range="bytes=0-4194303",TimeoutMS=16000)
                        warm=p.request(shared_url,Range="bytes=0-4194303",TimeoutMS=16000)
                        final=[m.stats(x) for x in processes]
                        row={"mode":mode,"round":round_index,"cold":cold,"warm":warm,"cpuSeconds":sum(a["cpuSeconds"]-b["cpuSeconds"] for a,b in zip(final,initial)),"workingSetBytes":sum(a["workingSetBytes"] for a in final)}
                        out["realBenchmarks"].append(row)
                        record(f"{mode}-same-video-{round_index}",all(r["ok"] and r["status"]==206 and r["bytes"]==4194304 for r in cold+warm))
                        stop(p,c)
                hashes={r["sha256"] for row in out["realBenchmarks"] for r in row["cold"]+row["warm"] if r["ok"]}
                record("same-video-body-matches",len(hashes)==1,distinctHashes=len(hashes))
            else:record("same-video-source-available",False)
        # Equal SS workload and dependency graph; alternate order for three pairs.
        fj=m.Job();manifest=run/"fixture.private.json";fp=fj.start([str(args.bin/"fixture.exe"),str(manifest)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,env=m.clean_env())
        if not m.waitfor(manifest.exists):raise RuntimeError("fixture-timeout")
        node=json.loads(manifest.read_text())["nodes"][0];raw=json.dumps({"proxies":[node]})
        for round_index in range(3):
            for mode in (("B","C") if round_index%2==0 else ("C","B")):
                p,c=start(mode,raw,dnsaddr);processes=[p.p]+([c.p] if c else [])
                idle=[m.stats(x) for x in processes];cold=p.request();warm=p.request(Iterations=40)
                start_time=time.perf_counter();before_stats=[m.stats(x) for x in processes]
                bulk=p.request("http://business.invalid/resource",Iterations=8,TimeoutMS=10000)
                elapsed=time.perf_counter()-start_time;after_stats=[m.stats(x) for x in processes]
                row={"mode":mode,"round":round_index,"startupMS":p.start_ms+(c.start_ms if c else 0),"idle":idle,"after":after_stats,"first":cold,"warm":warm,"bulk":bulk,"bulkWallSeconds":elapsed,"cpuSeconds":sum(a["cpuSeconds"]-b["cpuSeconds"] for a,b in zip(after_stats,before_stats))}
                out["benchmarks"].append(row)
                record(f"{mode}-benchmark-{round_index}",all(r["ok"] for r in cold+warm) and all(r["ok"] and r["bytes"]==16*1024*1024 and r["sha256"]=="080acf35a507ac9849cfcba47dc2ad83e01b75663a516279c8b9d243b719643e" for r in bulk))
                # Bounded sustained load, snapshots after every ten batches.
                if round_index==0:
                    samples=[];ok=True
                    for batch in range(30):
                        rs=p.request(Iterations=8,Concurrent=True);ok=ok and all(r["ok"] for r in rs)
                        if batch%10==0:samples.append(p.ask({"Stats":True}))
                    record(mode+"-sustained-240-requests",ok,samples=samples)
                stop(p,c)
        record("system-hosts-unchanged",before==m.sha(hosts))
    except BaseException as exc:
        # Keep only exception class; messages can contain a private URL or path.
        out["fatal"] = type(exc).__name__
        print("runner-failed",type(exc).__name__,flush=True)
    finally:
        for p in probes:p.close()
        for c in cores:c.close()
        if fj:fj.close()
        if fp:fp.wait(timeout=5)
        for s in servers:s.shutdown();s.server_close()
        out["cleanup"]={"probesExited":all(p.p.poll() is not None for p in probes),"coresExited":all(c.p.poll() is not None for c in cores),"providerPlaintextRemaining":len(list(run.rglob("p*.txt"))),"hostsUnchanged":before==m.sha(hosts)}
        (run/"results.json").write_text(json.dumps(out,indent=2)+"\n",encoding="utf-8")
        print("RESULT",run.relative_to(ROOT).as_posix(),flush=True)
    return int(bool(out.get("fatal")) or not all(c["passed"] for c in out["checks"]))

if __name__=="__main__":sys.exit(main())
