"""Sequential issue-14 increment. No product entry point, schema or system writes.

Private input stays in pipes/ACL-restricted ignored directories. Public output
contains only explicit measurements, fixed labels and sanitized error classes.
"""
import argparse
import base64
import hashlib
import http.server
import json
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import threading
import time

sys.dont_write_bytecode = True
import run as r
m = r.m


class Blackhole(m.socketserver.BaseRequestHandler):
    def handle(self):
        self.server.packets += 1


class Evaluation:
    def __init__(self, args):
        self.args = args
        # Explicit experimental factor: Python's historical default of five
        # pending accepts can itself reject a 40-way burst on Windows.
        m.TCPServer.request_queue_size = args.backlog
        http.server.ThreadingHTTPServer.request_queue_size = args.backlog
        self.root = args.bin / (args.phase + "-" + secrets.token_hex(5))
        m.private_dir(self.root)
        self.probes, self.cores, self.servers, self.jobs, self.children = [], [], [], [], []
        self.hosts = Path(m.os.environ["SystemRoot"]) / "System32/drivers/etc/hosts"
        self.before = m.sha(self.hosts)
        self.out = {"date":"2026-10-13", "phase":args.phase, "fixtureAcceptBacklog":args.backlog, "baseRevision":subprocess.check_output(
            ["git","rev-parse","HEAD"], cwd=r.ROOT, text=True).strip(), "checks":[], "benchmarks":[], "soak":[],
            "scope":"development only; no architecture selection or product migration",
            "binaries":{n:{"sha256":m.sha(args.bin/n), "bytes":(args.bin/n).stat().st_size}
                        for n in ("B.exe","C.exe","core.exe","fixture.exe")}}
        self.dns = self.server(m.DNSServer(("127.0.0.1",0),m.DNSFixture), queries=[])
        self.dnsaddr = "127.0.0.1:" + str(self.dns.server_address[1])

    def record(self, name, passed, **data):
        self.out["checks"].append({"name":name, "passed":bool(passed), **data})
        print(name, "PASS" if passed else "FAIL", flush=True)

    def server(self, server, **fields):
        for k,v in fields.items(): setattr(server,k,v)
        self.servers.append(server)
        return m.background(server)

    def prepare(self, payload):
        p = r.Probe(self.args.bin/"B.exe", {"Prepare":True,"Payload":payload})
        self.children.append(p.p)
        try: return p.ready
        finally: p.close()

    def start(self, mode, payload, dns=None):
        dns = dns or self.dnsaddr
        c = None
        if mode == "C":
            nodes = self.prepare(payload)
            if not nodes.get("accepted"): raise RuntimeError("input-rejected")
            c = m.Core(self.args.bin/"core.exe",self.root,
                       [json.dumps({"proxies":[n]}).encode() for n in nodes["nodes"]],len(nodes["nodes"]),dns,
                       mutate=lambda cfg:cfg.update({"log-level":"silent","find-process-mode":"off"}))
            self.cores.append(c); self.children.append(c.p)
            if not c.valid(): raise RuntimeError("mapping-invalid")
            init = {"Mode":"C", "Endpoints":c.endpoints()}
        else:
            home = self.root / (mode.lower()+"-"+secrets.token_hex(4)); home.mkdir()
            init = {"Mode":mode, "Payload":payload, "DNS":dns, "Home":str(home)}
        p = r.Probe(self.args.bin/("C.exe" if mode=="C" else "B.exe"),init)
        self.probes.append(p); self.children.append(p.p)
        if not p.ready.get("ready"): raise RuntimeError("probe-not-ready")
        return p,c

    def stop(self,p,c=None):
        p.close(); self.probes.remove(p)
        if c: c.close(); self.cores.remove(c)

    def fixture(self):
        job=m.Job();self.jobs.append(job)
        manifest=self.root/("fixture-"+secrets.token_hex(4)+".private.json")
        process=job.start([str(self.args.bin/"fixture.exe"),str(manifest)],
                         stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,env=m.clean_env())
        self.children.append(process)
        if not m.waitfor(manifest.exists): raise RuntimeError("fixture-timeout")
        return json.loads(manifest.read_text())

    def marked(self):
        fixtures=[]
        for marker in ("A","B"):
            h=self.server(http.server.ThreadingHTTPServer(("127.0.0.1",0),r.Fixture),
                          marker=marker,hits=0,streaming=threading.Event())
            fixtures.append(self.server(m.TCPServer(("127.0.0.1",0),m.SocksFixture),http=h,targets=[],fail=False))
        visible=m.socket_inventory(m.os.getpid())
        positive=any(x["LocalPort"]==fixtures[0].http.server_port for x in visible["tcp"])
        self.record("network-table-positive-control",positive)
        if not positive: raise RuntimeError("network-table-unavailable")
        nodes=[json.loads(m.socknode("DIRECT",s.server_address[1],"proxy.invalid"))["proxies"][0] for s in fixtures]
        return fixtures,nodes

    def lifecycle(self):
        fixtures,nodes=self.marked()
        payload=json.dumps({"proxies":nodes,"tun":{"enable":True},"rules":["MATCH,DIRECT"],"socks-port":54321})
        vmess={"v":"2","ps":"fixture","add":"127.0.0.1","port":"65534","id":"d342d11e-d424-4583-b36e-524ab1f0afa4","aid":"0","scy":"auto","net":"ws","path":"/socket","tls":""}
        uri="vmess://"+base64.b64encode(json.dumps(vmess).encode()).decode()
        for label,raw in [("uri",uri),("base64",base64.b64encode(uri.encode()).decode()),
                          ("yaml",json.dumps({"proxies":self.prepare(uri)["nodes"]}))]:
            p,c=self.start("B2",raw);self.record("B2-native-"+label,p.ready["channels"]==1);self.stop(p,c)
        for label,raw in [("empty","proxies: []"),("malformed","proxies: [null]"),
                          ("direct",json.dumps({"proxies":[{"type":"direct"}]}))]:
            self.record("B2-reject-"+label,not self.prepare(raw).get("accepted"))
        p,c=self.start("B2",payload)
        self.record("B2-no-listeners",m.socket_inventory(p.p.pid)["tcp"]==[])
        bindings=p.ask({"Bindings":True});self.record("B2-binding-negative",bindings["unknownAndBuiltinRejected"],details=bindings)
        rs=p.request(Iterations=20,Concurrent=True)
        fixed=lambda rows:len(rows)==40 and all(x["ok"] and x["marker"]==("A" if x["index"]==0 else "B") for x in rows)
        self.record("B2-40-concurrent-fixed-exits",fixed(rs),requests=rs)
        self.record("B2-name-rebind-injected",p.ask({"SwapNames":True})["swapped"])
        rs=p.request(Iterations=20,Concurrent=True)
        self.record("B2-captured-exits-ignore-rebind",fixed(rs),requests=rs)
        rs=p.request("http://business.invalid/range",Range="bytes=100-999")
        expected=hashlib.sha256((bytes(range(256))*16)[100:1000]).hexdigest()
        self.record("B2-range-body",len(rs)==2 and all(x["status"]==206 and x["bytes"]==900 and x["sha256"]==expected for x in rs))
        hits=sum(s.http.hits for s in fixtures)
        rs=p.request(PreCancel=True)
        self.record("B2-pre-cancel",all(not x["ok"] for x in rs) and hits==sum(s.http.hits for s in fixtures))
        for label,path,options in [("header","stall",{"TimeoutMS":150}),("body","stream",{"CancelAfterMS":150})]:
            rs=p.request("http://business.invalid/"+path,**options)
            self.record("B2-"+label+"-cancel",all(not x["ok"] and x["elapsedMS"]<1000 for x in rs),requests=rs)
        self.record("B2-reuse-after-cancel",all(x["ok"] for x in p.request()))
        state=p.ask({"Stats":True})
        self.record("B2-global-boundary",not state["systemHosts"] and state["standardResolverUnchanged"] and state["standardTransportUnchanged"],state=state)
        p.ask({"ShutdownOnly":True})
        self.record("B2-shutdown-alone-not-disposal",all(x["ok"] for x in p.request(CloseIdle=True)))
        retired=p.ask({"Retire":True,"URL":"http://business.invalid/api"})
        state=p.ask({"Stats":True})
        self.record("B2-retirement",retired["retired"] and state["admission"]["occupied"]==0,state=state)
        self.stop(p,c)
        fixtures[0].fail=True
        p,c=self.start("B2",json.dumps({"proxies":[nodes[0]]}))
        hits=fixtures[0].http.hits
        rs=p.request("http://127.0.0.1:"+str(fixtures[0].http.server_port)+"/api",TimeoutMS=500)
        self.record("B2-failed-proxy-no-direct",not rs[0]["ok"] and hits==fixtures[0].http.hits)
        self.stop(p,c)
        self.record("B2-target-DNS-remote","proxy.invalid" in self.dns.queries and "business.invalid" not in self.dns.queries)
        # Real upstream protocol implementations, owned by a separate fixture.
        protocol=self.fixture()
        for node in protocol["nodes"]:
            name=node["type"]
            p,c=self.start("B2",json.dumps({"proxies":[node]}))
            before=m.urllib.request.urlopen(protocol["statsURL"]).read()
            rs=p.request(RawDial=True,Address="business.invalid:80",Iterations=3)
            warmed=p.request(Iterations=4)
            after=m.urllib.request.urlopen(protocol["statsURL"]).read()
            self.record("B2-"+name+"-warm-session",all(x["ok"] for x in rs+warmed),serverConnections=json.loads(after)[name]-json.loads(before)[name])
            rs=p.request(RawDial=True,Address="business.invalid:80",PreCancel=True,Iterations=4)
            self.record("B2-"+name+"-warm-pre-cancel",all(not x["ok"] for x in rs))
            rs=p.request("http://business.invalid/stall",TimeoutMS=100,Concurrent=True,Iterations=8)
            self.record("B2-"+name+"-warm-HTTP-cancel",all(not x["ok"] and x["elapsedMS"]<1000 for x in rs),state=p.ask({"Stats":True}))
            self.record("B2-"+name+"-healthy-after-warm-cancel",all(x["ok"] for x in p.request()))
            retired=p.ask({"Retire":True,"URL":"http://business.invalid/api"})
            state=p.ask({"Stats":True})
            self.record("B2-"+name+"-retired",retired["retired"] and state["admission"]["occupied"]==0 and state["coreTracked"]==0,state=state,network=m.socket_inventory(p.p.pid))
            if name=="anytls":
                time.sleep(31)
                self.out["anytlsAfter31Seconds"]=p.ask({"Stats":True})
            self.stop(p,c)
        stall=self.server(m.TCPServer(("127.0.0.1",0),r.Stall),lock=threading.Lock(),active=0,peak=0)
        udp=self.server(m.DNSServer(("127.0.0.1",0),Blackhole),packets=0)
        for typ in ("socks5","vless","anytls","hysteria2"):
            node={"type":typ,"server":"127.0.0.1","port":udp.server_address[1] if typ=="hysteria2" else stall.server_address[1]}
            if typ=="vless":node.update(uuid="d342d11e-d424-4583-b36e-524ab1f0afa4",tls=True,**{"skip-cert-verify":True})
            if typ in ("anytls","hysteria2"):node.update(password="fixture",**{"skip-cert-verify":True})
            p,c=self.start("B2",json.dumps({"proxies":[node]}));samples=[]
            direct=p.request(RawDial=True,Address="business.invalid:80",TimeoutMS=100)
            self.record("B2-"+typ+"-slow-handshake-cancel",not direct[0]["ok"] and direct[0]["elapsedMS"]<1000,requests=direct)
            all_failed=True
            for _ in range(6):
                rs=p.request(Iterations=32,Concurrent=True,TimeoutMS=100)
                all_failed=all_failed and len(rs)==32 and all(not x["ok"] and x["elapsedMS"]<1000 for x in rs)
                samples.append(p.ask({"Stats":True}))
            time.sleep(4)
            final=p.ask({"Stats":True})
            self.record("B2-"+typ+"-detached-dials-bounded",all_failed and final["admission"]["pending"]==0 and all(s["admission"]["peak"]<=64 for s in samples),samples=samples,final=final)
            p.ask({"Retire":True,"URL":"http://business.invalid/api"})
            self.out.setdefault("slowHandshakeRetirement",[]).append({"protocol":typ,"state":p.ask({"Stats":True}),"network":m.socket_inventory(p.p.pid)})
            self.stop(p,c)
        p,c=self.start("B2",json.dumps({"proxies":[nodes[1]]}))
        p.p.stdin.write('{"Panic":true}\n');p.p.stdin.flush();p.p.wait(timeout=5)
        self.record("B2-synthetic-panic-ends-host",p.p.returncode!=0);self.stop(p,c)

    def close(self):
        for p in self.probes:p.close()
        for c in self.cores:c.close()
        for j in self.jobs:j.close()
        for p in self.children:p.wait(timeout=5)
        for s in self.servers:s.shutdown();s.server_close()
        self.out["cleanup"]={"allOwnedProcessesExited":all(p.poll() is not None for p in self.children),
                             "providerFilesRemaining":len(list(self.root.rglob("p*.txt"))),"hostsUnchanged":self.before==m.sha(self.hosts)}
        (self.root/"results.json").write_text(json.dumps(self.out,indent=2)+"\n",encoding="utf-8")
        print("RESULT",self.root.relative_to(r.ROOT).as_posix(),flush=True)

    def bindings(self):
        fixtures,nodes=self.marked()
        for mode in ("B2","B","C"):
            p,c=self.start(mode,json.dumps({"proxies":nodes}))
            for i in range(2):
                if i==1 and mode=="B2":p.ask({"SwapNames":True})
                rs=p.request(Iterations=20,Concurrent=True,CloseIdle=True)
                self.record(mode+"-concurrent-"+str(i),len(rs)==40 and all(x["ok"] and x["marker"]==("A" if x["index"]==0 else "B") for x in rs),requests=rs,state=p.ask({"Stats":True}))
            self.stop(p,c)


def main():
    ap=argparse.ArgumentParser();ap.add_argument("--bin",type=Path,required=True)
    ap.add_argument("--phase",choices=("lifecycle","compare","bindings"),required=True)
    ap.add_argument("--private-subscription",type=Path)
    ap.add_argument("--backlog",type=int,default=128,choices=(5,128))
    args=ap.parse_args();e=Evaluation(args)
    try:
        if args.phase=="lifecycle":e.lifecycle()
        elif args.phase=="bindings":e.bindings()
        else:raise NotImplementedError("comparison not implemented yet")
    except BaseException as exc:
        e.out["fatal"]=type(exc).__name__;print("RUNNER",type(exc).__name__,flush=True)
    finally:e.close()
    return int(bool(e.out.get("fatal")) or not e.out["checks"] or not all(x["passed"] for x in e.out["checks"]))

if __name__=="__main__":sys.exit(main())
