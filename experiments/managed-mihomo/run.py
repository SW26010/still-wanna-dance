"""Windows development experiment, NOT a production backend or an importer.

Runs a pinned, separately built upstream core and the actual SWD SOCKS/MultiPool
driver. Inputs and core output stay in an ACL-restricted ignored directory.
Only explicitly constructed, nonsensitive observations enter results.json.
"""
import argparse
import base64
import concurrent.futures
import ctypes as C
from ctypes import wintypes as W
import hashlib
import http.server
import json
import os
from pathlib import Path
import platform
import secrets
import socket
import socketserver
import struct
import subprocess
import sys
import threading
import time
import urllib.request
import urllib.error

ROOT = Path(__file__).resolve().parents[2]
HOME = ROOT / "artifacts/issue14/managed-core-prototype"
K32 = C.WinDLL("kernel32", use_last_error=True)
PSAPI = C.WinDLL("psapi", use_last_error=True)

class BasicLimit(C.Structure):
    _fields_ = [("user", C.c_int64), ("job", C.c_int64), ("flags", W.DWORD),
                ("minws", C.c_size_t), ("maxws", C.c_size_t), ("active", W.DWORD),
                ("affinity", C.c_size_t), ("priority", W.DWORD), ("scheduling", W.DWORD)]
class IOStats(C.Structure):
    _fields_ = [(x, C.c_uint64) for x in ("readops", "writeops", "otherops", "read", "write", "other")]
class ExtendedLimit(C.Structure):
    _fields_ = [("basic", BasicLimit), ("io", IOStats), ("processmem", C.c_size_t),
                ("jobmem", C.c_size_t), ("peakprocess", C.c_size_t), ("peakjob", C.c_size_t)]
class ThreadEntry(C.Structure):
    _fields_ = [("size", W.DWORD), ("usage", W.DWORD), ("tid", W.DWORD),
                ("pid", W.DWORD), ("priority", W.LONG), ("base", W.LONG), ("flags", W.DWORD)]
class Memory(C.Structure):
    _fields_ = [("cb", W.DWORD), ("faults", W.DWORD)] + [(x, C.c_size_t) for x in
                 ("peakws", "ws", "peakpaged", "paged", "peaknonpaged", "nonpaged", "pagefile", "peakpagefile", "private")]
K32.CreateJobObjectW.restype = W.HANDLE
K32.CreateJobObjectW.argtypes = [C.c_void_p, W.LPCWSTR]
K32.SetInformationJobObject.argtypes = [W.HANDLE, C.c_int, C.c_void_p, W.DWORD]
K32.AssignProcessToJobObject.argtypes = [W.HANDLE, W.HANDLE]
K32.CloseHandle.argtypes = [W.HANDLE]
K32.CreateToolhelp32Snapshot.argtypes = [W.DWORD, W.DWORD]
K32.CreateToolhelp32Snapshot.restype = W.HANDLE
K32.Thread32First.argtypes = [W.HANDLE, C.POINTER(ThreadEntry)]
K32.Thread32Next.argtypes = [W.HANDLE, C.POINTER(ThreadEntry)]
K32.OpenThread.argtypes = [W.DWORD, W.BOOL, W.DWORD]
K32.OpenThread.restype = W.HANDLE
K32.ResumeThread.argtypes = [W.HANDLE]
K32.ResumeThread.restype = W.DWORD
K32.OpenProcess.argtypes = [W.DWORD, W.BOOL, W.DWORD]
K32.OpenProcess.restype = W.HANDLE
K32.GetProcessTimes.argtypes = [W.HANDLE] + [C.POINTER(C.c_uint64)] * 4
PSAPI.GetProcessMemoryInfo.argtypes = [W.HANDLE, C.POINTER(Memory), W.DWORD]

def checked(value):
    if not value:
        raise C.WinError(C.get_last_error())
    return value

class Job:
    def __init__(self):
        self.handle = checked(K32.CreateJobObjectW(None, None))
        info = ExtendedLimit()
        info.basic.flags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        checked(K32.SetInformationJobObject(self.handle, 9, C.byref(info), C.sizeof(info)))

    def start(self, args, **kwargs):
        # Assign before executing ANY child code. Popen closes its primary thread
        # handle; retrieve the suspended thread through the documented Toolhelp API.
        p = subprocess.Popen(args, creationflags=0x08000000 | 4, **kwargs)
        try:
            checked(K32.AssignProcessToJobObject(self.handle, int(p._handle)))
            snapshot = K32.CreateToolhelp32Snapshot(4, 0)
            if snapshot == C.c_void_p(-1).value:
                raise C.WinError(C.get_last_error())
            found = False
            try:
                entry = ThreadEntry(); entry.size = C.sizeof(entry)
                ok = K32.Thread32First(snapshot, C.byref(entry))
                while ok:
                    if entry.pid == p.pid:
                        th = checked(K32.OpenThread(2, False, entry.tid))
                        try:
                            if K32.ResumeThread(th) == 0xffffffff:
                                raise C.WinError(C.get_last_error())
                        finally:
                            K32.CloseHandle(th)
                        found = True
                        break
                    ok = K32.Thread32Next(snapshot, C.byref(entry))
            finally:
                K32.CloseHandle(snapshot)
            if not found:
                raise RuntimeError("suspended child thread missing")
            return p
        except BaseException:
            p.kill(); p.wait(); raise

    def close(self):
        if self.handle:
            checked(K32.CloseHandle(self.handle)); self.handle = None

def stats(p):
    handle = int(p._handle)
    m = Memory(); m.cb = C.sizeof(m)
    checked(PSAPI.GetProcessMemoryInfo(handle, C.byref(m), C.sizeof(m)))
    a, b, kernel, user = (C.c_uint64() for _ in range(4))
    checked(K32.GetProcessTimes(handle, C.byref(a), C.byref(b), C.byref(kernel), C.byref(user)))
    return {"workingSetBytes": m.ws, "privateBytes": m.private,
            "cpuSeconds": (kernel.value + user.value) / 1e7}

def alive(pid):
    h = K32.OpenProcess(0x1000, False, pid)
    if not h:
        return False
    try:
        code = W.DWORD()
        K32.GetExitCodeProcess.argtypes = [W.HANDLE, C.POINTER(W.DWORD)]
        return bool(K32.GetExitCodeProcess(h, C.byref(code))) and code.value == 259
    finally:
        K32.CloseHandle(h)

def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0)); return s.getsockname()[1]

def waitfor(fn, seconds=5):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if fn(): return True
        time.sleep(.03)
    return False

def private_dir(path):
    path.mkdir(parents=True, exist_ok=False)
    sid = subprocess.check_output(["whoami", "/user", "/fo", "csv", "/nh"], text=True).strip().split(",")[-1].strip('"')
    subprocess.run(["icacls", str(path), "/inheritance:r", "/grant:r", f"*{sid}:(OI)(CI)F", "*S-1-5-18:(OI)(CI)F"],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)

def clean_env():
    return {k:v for k,v in os.environ.items() if not k.upper().startswith(("CLASH_", "MIHOMO_"))
            and k.upper() not in ("SAFE_PATHS", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY")}

class Core:
    counter = 0
    def __init__(self, core, root, payloads=None, endpoints=0, dns=None, mutate=None, test=False):
        Core.counter += 1
        self.dir = root / f"core-{Core.counter:03d}"
        self.dir.mkdir()
        self.job = Job()
        self.controller = port(); self.secret = secrets.token_hex(24)
        self.ports = [port() for _ in range(endpoints)]
        self.username = secrets.token_hex(12); self.password = secrets.token_hex(24)
        self.payloads = payloads or []
        providers = {}
        for i, data in enumerate(self.payloads):
            (self.dir / f"p{i}.txt").write_bytes(data)
            providers[f"p{i}"] = {"type":"file", "path":f"p{i}.txt", "interval":0, "health-check":{"enable":False}}
        self.config = {"mode":"rule", "log-level":"error", "allow-lan":False,
            "bind-address":"127.0.0.1", "port":0, "socks-port":0, "mixed-port":0, "redir-port":0, "tproxy-port":0,
            "external-controller":f"127.0.0.1:{self.controller}", "secret":self.secret,
            "external-controller-cors":{"allow-origins":[], "allow-private-network":False},
            "tun":{"enable":False}, "sniffer":{"enable":False}, "ntp":{"enable":False}, "geo-auto-update":False,
            "profile":{"store-selected":False, "store-fake-ip":False},
            "dns":{"enable":True, "listen":"", "ipv6":False, "use-hosts":False, "use-system-hosts":False,
                   "enhanced-mode":"redir-host", "default-nameserver":[dns or "1.1.1.1"],
                   "nameserver":[dns or "1.1.1.1"], "proxy-server-nameserver":[dns or "1.1.1.1"]},
            "proxy-providers":providers,
            "proxy-groups":[{"name":f"g{i}", "type":"select", "use":[f"p{i}"], "empty-fallback":"REJECT"} for i in range(len(providers))],
            "listeners":[{"name":f"in{i}", "type":"socks", "listen":"127.0.0.1", "port":p,
                          "udp":False, "proxy":f"g{i}", "users":[{"username":self.username,"password":self.password}]} for i,p in enumerate(self.ports)],
            "rules":["MATCH,REJECT"]}
        if mutate: mutate(self.config)
        self.log = (self.dir / "core.private.log").open("wb")
        start = time.monotonic()
        args = [str(core), "-d", str(self.dir), "-f", "-"] + (["-t"] if test else [])
        self.p = self.job.start(args, stdin=subprocess.PIPE, stdout=self.log, stderr=subprocess.STDOUT, env=clean_env())
        try:
            self.p.stdin.write(json.dumps(self.config).encode()); self.p.stdin.close()
        except BrokenPipeError:
            pass
        self.ready = False if test else waitfor(self.isready)
        self.start_ms = (time.monotonic()-start)*1000

    def api(self, path, auth=True):
        req = urllib.request.Request(f"http://127.0.0.1:{self.controller}{path}", headers={"Authorization":"Bearer "+self.secret} if auth else {})
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=.5) as r:
            return json.load(r)

    def isready(self):
        if self.p.poll() is not None: return False
        try: return self.api("/version").get("version") == "v1.19.32"
        except (OSError, ValueError): return False

    def counts(self):
        ps = self.api("/providers/proxies")["providers"]
        return [len(ps.get(f"p{i}",{}).get("proxies",[])) for i in range(len(self.payloads))]

    def valid(self):
        if not self.ready or self.counts() != [1]*len(self.payloads): return False
        for i in range(len(self.payloads)):
            provider = self.api(f"/providers/proxies/p{i}")["proxies"][0]
            group = self.api(f"/proxies/g{i}")
            if group.get("all") != [provider["name"]] or group.get("now") != provider["name"]: return False
        return True

    def endpoints(self):
        return [{"Address":f"127.0.0.1:{p}","Username":self.username,"Password":self.password} for p in self.ports]

    def close(self):
        self.job.close(); self.p.wait(timeout=5); self.log.close()
        # Remove only this run's known provider plaintext. Raw diagnostics are private.
        for i in range(len(self.payloads)): (self.dir / f"p{i}.txt").unlink(missing_ok=True)

class Probe:
    def __init__(self, binary, endpoints, mode="managed"):
        self.job = Job()
        self.p = self.job.start([str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, encoding="utf-8")
        self.ready = self.ask({"Mode":mode,"Endpoints":endpoints})
        if not self.ready.get("ready"): raise RuntimeError("probe initialization failed")
    def ask(self, command):
        self.p.stdin.write(json.dumps(command)+"\n"); self.p.stdin.flush()
        return json.loads(self.p.stdout.readline())
    def request(self, url="http://business.invalid/api", **kw):
        return self.ask({"URL":url,**kw})
    def close(self):
        self.p.stdin.close(); self.p.wait(timeout=5); self.job.close()

class HTTPFixture(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *args): pass
    def do_GET(self):
        self.server.hits += 1
        if self.path == "/stream":
            self.send_response(200);self.send_header("Content-Length","10000");self.end_headers()
            self.wfile.write(b"x");self.wfile.flush();self.server.streaming.set();time.sleep(2);return
        if self.path == "/stall":
            time.sleep(1); return
        self.send_response(200); self.send_header("X-SWD-Fixture", self.server.marker)
        data = b"fixture-" + self.server.marker.encode()
        self.send_header("Content-Length",str(len(data))); self.end_headers()
        try: self.wfile.write(data)
        except OSError: pass

def exact(s, n):
    result = b""
    while len(result)<n:
        b=s.recv(n-len(result))
        if not b: raise EOFError()
        result+=b
    return result

class SocksFixture(socketserver.BaseRequestHandler):
    def handle(self):
        try:
            s=self.request; s.settimeout(4)
            _, n=exact(s,2); exact(s,n); s.sendall(b"\x05\x00")
            ver,cmd,_,atyp=exact(s,4)
            if atyp==3: host=exact(s,exact(s,1)[0]).decode()
            elif atyp==1: host=socket.inet_ntoa(exact(s,4))
            else: return
            target_port=struct.unpack("!H",exact(s,2))[0]
            if cmd!=1 or (host,target_port) not in [("business.invalid",80),("127.0.0.1",self.server.http.server_port)]: return
            self.server.targets.append(host)
            if self.server.fail: s.sendall(b"\x05\x05\x00\x01"+b"\x00"*6); return
            with socket.create_connection(("127.0.0.1",self.server.http.server_port),2) as other:
                s.sendall(b"\x05\x00\x00\x01"+b"\x00"*6)
                def forward():
                    try:
                        while True:
                            b=s.recv(65536)
                            if not b: break
                            other.sendall(b)
                    except OSError: pass
                    try: other.shutdown(socket.SHUT_WR)
                    except OSError: pass
                t=threading.Thread(target=forward,daemon=True);t.start()
                try:
                    while True:
                        b=other.recv(65536)
                        if not b: break
                        s.sendall(b)
                except OSError: pass
        except (OSError,EOFError): pass

class TCPServer(socketserver.ThreadingTCPServer):
    daemon_threads=True
class DNSServer(socketserver.ThreadingUDPServer):
    daemon_threads=True
class DNSFixture(socketserver.BaseRequestHandler):
    def handle(self):
        data,s=self.request; pos=12; labels=[]
        while data[pos]:
            n=data[pos]; labels.append(data[pos+1:pos+1+n].decode()); pos+=n+1
        name=".".join(labels); self.server.queries.append(name); end=pos+5
        typ=struct.unpack("!H",data[pos+1:pos+3])[0]
        answer = name in ("proxy.invalid", "extra.invalid") and typ==1
        packet=data[:2]+b"\x81\x80"+b"\x00\x01"+struct.pack("!H",int(answer))+b"\x00"*4+data[12:end]
        if answer: packet+=b"\xc0\x0c\x00\x01\x00\x01\x00\x00\x00\x01\x00\x04\x7f\x00\x00\x01"
        s.sendto(packet,self.client_address)

def background(server):
    threading.Thread(target=server.serve_forever,daemon=True).start(); return server

def socknode(name,p,host="127.0.0.1"):
    return json.dumps({"proxies":[{"name":name,"type":"socks5","server":host,"port":p}]}).encode()

def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()

def socket_inventory(pid):
    rows=subprocess.check_output(["netstat","-ano"],creationflags=0x08000000).decode("ascii",errors="ignore").splitlines()
    result={"tcp":[],"udp":[]}
    for line in rows:
        fields=line.split()
        if len(fields)<4 or fields[-1]!=str(pid): continue
        if fields[0]=="TCP" and fields[3]!="LISTENING": continue
        if fields[0] not in ("TCP","UDP"): continue
        address,p=fields[1].rsplit(":",1)
        result[fields[0].lower()].append({"LocalAddress":address,"LocalPort":int(p)})
    return result

def main():
    ap=argparse.ArgumentParser(); ap.add_argument("--core",type=Path,required=True); ap.add_argument("--probe",type=Path)
    ap.add_argument("--fixture",type=Path); ap.add_argument("--private-subscription",type=Path)
    ap.add_argument("--job-child",type=Path); args=ap.parse_args()
    if args.job_child:
        c=Core(args.core,args.job_child)
        print(json.dumps({"pid":c.p.pid,"ready":c.ready}),flush=True)
        sys.stdin.read(); c.close(); return
    run=HOME/("run-"+secrets.token_hex(5)); private_dir(run)
    out={"baseRevision":subprocess.check_output(["git","rev-parse","HEAD"],cwd=ROOT,text=True).strip(),
         "platform":platform.platform(),"date":"2026-10-13","coreSHA256":sha(args.core),"probeSHA256":sha(args.probe),
         "scope":"isolated Windows development experiment; no game, saved settings, hosts or system proxy writes",
         "checks":[],"benchmarks":[]}
    def record(name, passed, **details):
        out["checks"].append({"name":name,"passed":passed,**details}); print(name, "PASS" if passed else "FAIL",flush=True)
        (run/"results.json").write_text(json.dumps(out,indent=2)+"\n",encoding="utf-8")
    hosts=Path(os.environ["SystemRoot"])/"System32/drivers/etc/hosts"; hosts_before=sha(hosts)
    import winreg
    def proxy_settings():
        with winreg.OpenKey(winreg.HKEY_CURRENT_USER,r"Software\Microsoft\Windows\CurrentVersion\Internet Settings") as k:
            result={}
            for n in ("ProxyEnable","ProxyServer","AutoConfigURL"):
                try: result[n]=winreg.QueryValueEx(k,n)[0]
                except FileNotFoundError: pass
            return result
    proxy_before=proxy_settings()
    adapters_before=subprocess.check_output(["netsh","interface","show","interface"])
    winhttp_before=subprocess.check_output(["netsh","winhttp","show","proxy"])
    servers=[]; cores=[]; probes=[]; fixture_job=None
    try:
        dns=background(DNSServer(("127.0.0.1",0),DNSFixture));dns.queries=[];servers.append(dns)
        dnsaddr=f"127.0.0.1:{dns.server_address[1]}"
        fixtures=[]
        for marker in ("A","B"):
            h=http.server.ThreadingHTTPServer(("127.0.0.1",0),HTTPFixture);h.marker=marker;h.hits=0;h.streaming=threading.Event();background(h);servers.append(h)
            s=TCPServer(("127.0.0.1",0),SocksFixture);s.http=h;s.targets=[];s.fail=False;background(s);servers.append(s);fixtures.append(s)
        payloads=[socknode("node-"+str(i),s.server_address[1], "extra.invalid" if i else "proxy.invalid") for i,s in enumerate(fixtures)]
        c=Core(args.core,run,payloads,2,dnsaddr);cores.append(c)
        record("version-provider-singleton-group-mapping",c.valid(),counts=c.counts(),startupMS=c.start_ms)
        sockets=socket_inventory(c.p.pid)
        record("loopback-listeners-no-udp-inbound",all(s["LocalAddress"]=="127.0.0.1" for s in sockets["tcp"]) and sorted(s["LocalPort"] for s in sockets["tcp"])==sorted(c.ports+[c.controller]) and not any(s["LocalPort"] in c.ports for s in sockets["udp"]),tcpListeners=len(sockets["tcp"]),udpOnSocksPorts=any(s["LocalPort"] in c.ports for s in sockets["udp"]))
        try: c.api("/version",False); denied=False
        except urllib.error.HTTPError as e: denied=e.code==401
        record("controller-requires-secret",denied)
        p=Probe(args.probe,c.endpoints());probes.append(p)
        result=p.request(Iterations=10,Concurrent=True)["observations"]
        record("two-fixed-exits-concurrent",all(r["ok"] and r["marker"]==("A","B")[r["index"]] for r in result),requests=result)
        record("additional-proxy-server-dns",{"proxy.invalid","extra.invalid"}<=set(dns.queries),queries=sorted(set(dns.queries)))
        result=p.request("http://business.invalid/stall",TimeoutMS=200,Concurrent=True)["observations"]
        record("request-cancellation",all(not r["ok"] and r["elapsedMS"]<800 for r in result),requests=result)
        wrong=c.endpoints();wrong[0]["Password"]="wrong";w=Probe(args.probe,wrong[:1]);probes.append(w)
        result=w.request()["observations"]
        record("socks-requires-credentials",all(not r["ok"] for r in result),requests=result)
        # A directly reachable target is the tripwire: fallback would increment hits.
        fixtures[0].fail=True
        before=fixtures[0].http.hits
        result=p.request(f"http://127.0.0.1:{fixtures[0].http.server_port}/api")["observations"]
        record("failed-node-no-direct",not result[0]["ok"] and before==fixtures[0].http.hits,requests=result)
        fixtures[0].fail=False
        for label, data in [("empty",b"proxies: []\n"),("multi",json.dumps({"proxies":[{"name":"x","type":"socks5","server":"127.0.0.1","port":fixtures[0].server_address[1]},{"name":"y","type":"socks5","server":"127.0.0.1","port":fixtures[1].server_address[1]}]}).encode())]:
            if label=="empty":
                syntax=Core(args.core,run,[data],test=True);cores.append(syntax)
                syntax.p.wait(timeout=5)
                record("syntax-success-does-not-prove-provider-load",syntax.p.returncode==0,syntaxExitCode=syntax.p.returncode)
            v=Core(args.core,run,[data],1,dnsaddr);cores.append(v)
            record(label+"-provider-publication-refused",not v.valid(),counts=v.counts())
            if label=="empty":
                vp=Probe(args.probe,v.endpoints());probes.append(vp);before=fixtures[0].http.hits
                result=vp.request(f"http://127.0.0.1:{fixtures[0].http.server_port}/api")["observations"]
                record("empty-provider-reject-fallback",not result[0]["ok"] and before==fixtures[0].http.hits,requests=result)
        v=Core(args.core,run,payloads[:1],1,dnsaddr,mutate=lambda cfg:cfg["listeners"][0].update(proxy="missing"));cores.append(v)
        vp=Probe(args.probe,v.endpoints());probes.append(vp);before=fixtures[0].http.hits
        result=vp.request(f"http://127.0.0.1:{fixtures[0].http.server_port}/api")["observations"]
        record("invalid-listener-proxy-no-direct",not result[0]["ok"] and before==fixtures[0].http.hits,requests=result,coreAPIReady=v.ready)
        # A foreign listener cannot satisfy SOCKS authentication merely by holding the port.
        blocker=socket.socket();blocker.bind(("127.0.0.1",0));blocker.listen()
        try:
            v=Core(args.core,run,payloads[:1],1,dnsaddr,mutate=lambda cfg:cfg["listeners"][0].update(port=blocker.getsockname()[1]));cores.append(v)
            v.ports=[blocker.getsockname()[1]]
            vp=Probe(args.probe,v.endpoints());probes.append(vp)
            result=vp.request(TimeoutMS=200)["observations"]
            record("port-conflict-not-ready-for-publication",not result[0]["ok"],coreAPIReady=v.ready,requests=result)
        finally: blocker.close()
        v=Core(args.core,run,mutate=lambda cfg:cfg.update({"proxy-groups":[{"name":"bad","type":"nonexistent"}]}));cores.append(v)
        record("invalid-config-no-ready",not v.ready,exitCode=v.p.poll())
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            pending=executor.submit(p.request,"http://business.invalid/stream",TimeoutMS=3000,Concurrent=True)
            streaming=all(s.http.streaming.wait(2) for s in fixtures)
            killed=time.monotonic();c.p.kill();c.p.wait(timeout=5)
            active=pending.result(timeout=4)["observations"]
            record("core-crash-interrupts-active-bodies",streaming and all(not r["ok"] for r in active) and time.monotonic()-killed<1,requests=active)
        result=p.request()["observations"]
        retired=p.request(Retire=True)
        replacement=Core(args.core,run,payloads,2,dnsaddr);cores.append(replacement)
        record("crash-retire-restart-new-generation",all(not r["ok"] for r in result) and retired["retired"] and replacement.valid() and set(c.ports).isdisjoint(replacement.ports),requests=result)
        # Abrupt owner death closes its Job handle; the core was assigned suspended.
        ownerdir=run/"owner";ownerdir.mkdir()
        owner=subprocess.Popen([sys.executable,str(Path(__file__).resolve()),"--core",str(args.core),"--job-child",str(ownerdir)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,creationflags=0x08000000)
        child=json.loads(owner.stdout.readline());owner.kill();owner.wait()
        record("owner-killed-no-orphan",child["ready"] and waitfor(lambda:not alive(child["pid"])))
        # Native URI and Base64 are given intact to providers, never protocol-translated.
        ssuri="ss://"+base64.b64encode(b"aes-128-gcm:fixture-password").decode()+"@127.0.0.1:65534#fixture"
        yaml=b"proxies:\n  - name: fixture\n    type: ss\n    server: 127.0.0.1\n    port: 65534\n    cipher: aes-128-gcm\n    password: fixture-password\n"
        v=Core(args.core,run,[yaml,ssuri.encode(),base64.b64encode(ssuri.encode())]);cores.append(v)
        record("native-yaml-uri-base64-load",v.valid(),counts=v.counts(),connectivity="not-attempted")
        if args.private_subscription:
            raw=args.private_subscription.read_bytes().strip()
            lines=base64.b64decode(raw).decode().splitlines()
            entries=[x.strip().encode() for x in lines if x.strip()]
            v=Core(args.core,run,entries);cores.append(v)
            counts=v.counts()
            record("private-native-single-entry-providers",v.valid(),inputEntries=len(entries),loadedEntries=sum(x==1 for x in counts),counts=counts,connectivity="not-attempted")
            full=Core(args.core,run,[raw]);cores.append(full)
            record("private-native-base64-whole-provider",full.counts()==[len(entries)],counts=full.counts(),connectivity="not-attempted")
            # A max-two active generation; bounded connectivity probe, separate from loading.
            live=Core(args.core,run,entries[:2],2);cores.append(live)
            if live.valid():
                lp=Probe(args.probe,live.endpoints());probes.append(lp)
                result=lp.request("https://example.com/",TimeoutMS=8000,Concurrent=True)["observations"]
                record("private-first-two-https-connectivity",all(r["ok"] for r in result),requests=result,scope="public HTTPS diagnostic only, not SWD API/video")
                result=lp.ask({"Monitor":True})
                resource=[r for r in result.get("results",[]) if r["operation"]=="resource"]
                record("private-swd-monitor-and-selected-business",result.get("completed",False) and result.get("businessUsesSelectedTransport",False) and result.get("businessStatus")==302 and any(r["state"]=="available" and r["bytes"]>0 for r in resource),observation=result)
        # Close every compatibility core before measuring, so idle test cores do not skew CPU/RSS.
        for pp in probes: pp.close()
        probes=[]
        for cc in cores: cc.close()
        cores=[]
        if args.fixture:
            fixture_job=Job();manifest=run/"fixture.private.json"
            fp=fixture_job.start([str(args.fixture),str(manifest)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            if not waitfor(manifest.exists): raise RuntimeError("fixture not ready")
            node=json.loads(manifest.read_text())["nodes"][0]
            for round_index in range(3):
                for mode in (("embedded","managed") if round_index%2==0 else ("managed","embedded")):
                    cc=None
                    if mode=="managed":
                        cc=Core(args.core,run,[json.dumps({"proxies":[node]}).encode()],1,dnsaddr);cores.append(cc)
                        if not cc.valid(): raise RuntimeError("benchmark core mapping unavailable")
                        ep=cc.endpoints()
                    else: ep=[{"Parameters":node}]
                    pp=Probe(args.probe,ep,mode);probes.append(pp)
                    processes=[pp.p]+([cc.p] if cc else [])
                    idle=[stats(x) for x in processes]
                    first=pp.request()["observations"]
                    small=pp.request(Iterations=40)["observations"]
                    start=time.perf_counter();before=[stats(x) for x in processes]
                    bulk=pp.request("http://business.invalid/resource",Iterations=8,TimeoutMS=10000)["observations"]
                    elapsed=time.perf_counter()-start;after=[stats(x) for x in processes]
                    row={"round":round_index,"mode":mode,"coreStartupMS":cc.start_ms if cc else None,"idle":idle,"after":after,
                         "cpuSeconds":sum(x["cpuSeconds"]-y["cpuSeconds"] for x,y in zip(after,before)),"bulkWallSeconds":elapsed,
                         "first":first,"small":small,"bulk":bulk}
                    out["benchmarks"].append(row)
                    record(f"benchmark-{round_index}-{mode}",all(r["ok"] and r["bytes"]==16*1024*1024 and r["sha256"]=="080acf35a507ac9849cfcba47dc2ad83e01b75663a516279c8b9d243b719643e" for r in bulk))
                    pp.close();probes.remove(pp)
                    if cc: cc.close();cores.remove(cc)
        record("system-hosts-and-proxy-unchanged",hosts_before==sha(hosts) and proxy_before==proxy_settings())
        record("network-interface-table-and-winhttp-proxy-unchanged",adapters_before==subprocess.check_output(["netsh","interface","show","interface"]) and winhttp_before==subprocess.check_output(["netsh","winhttp","show","proxy"]))
    finally:
        for pp in probes: pp.close()
        pids=[cc.p.pid for cc in cores]
        for cc in cores: cc.close()
        if fixture_job: fixture_job.close();fp.wait(timeout=5)
        for s in servers: s.shutdown();s.server_close()
        out["cleanup"]={"trackedCoresExited":all(not alive(pid) for pid in pids),"providerPlaintextFilesRemaining":len(list(run.rglob("p*.txt"))),
                        "rawLogs":"ACL-restricted ignored run directory", "systemHostsUnchanged":hosts_before==sha(hosts), "systemProxyUnchanged":proxy_before==proxy_settings()}
        (run/"results.json").write_text(json.dumps(out,indent=2)+"\n",encoding="utf-8")
        print("RESULT",run.relative_to(ROOT).as_posix(),flush=True)
    return 0 if all(c["passed"] for c in out["checks"]) else 1

if __name__=="__main__": sys.exit(main() or 0)
