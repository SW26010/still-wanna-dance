"""Export only sanitized measurements; never read a subscription or core log."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
DEST = ROOT / "docs/evidence/issue14-incremental-20261013"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write(path, data):
    path.write_text(json.dumps(data,indent=2,ensure_ascii=False)+"\n",encoding="utf-8",newline="\n")


def screen(data):
    serialized=json.dumps(data)
    if re.search(r'://|(?i:authorization|privateResourceURL|ciphertext|password)|[A-Za-z]:\\\\',serialized):
        raise ValueError("privacy-screen-refused")


def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("--result",action="append",default=[],help="public-label=private-results.json")
    ap.add_argument("--bin",type=Path)
    ap.add_argument("--go")
    ap.add_argument("--build-label",default="build-identity")
    args=ap.parse_args();DEST.mkdir(parents=True,exist_ok=True)
    for item in args.result:
        label,path=item.split("=",1)
        if not re.fullmatch(r"[a-z0-9-]+",label):raise ValueError("invalid-label")
        data=json.loads(Path(path).read_text(encoding="utf-8"));screen(data)
        target=DEST/(label+".json")
        if target.exists():raise ValueError("refusing-to-overwrite-evidence")
        write(target,data)
    if args.bin:
        if not args.go or not re.fullmatch(r"[a-z0-9-]+",args.build_label):raise ValueError("build-arguments")
        identity={}
        for name in ("B.exe","C.exe","core.exe","fixture.exe"):
            path=args.bin/name
            raw=subprocess.check_output([args.go,"version","-m",str(path)],text=True)
            identity[name]={"sha256":digest(path.read_bytes()),"bytes":path.stat().st_size,
                            "metadata":[s.strip() for s in raw.splitlines()[1:]]}
        screen(identity)
        target=DEST/(args.build_label+".json")
        if target.exists():raise ValueError("refusing-to-overwrite-build-identity")
        write(target,identity)
    files=sorted(p for p in DEST.glob("*.json") if p.name!="manifest.json")
    source=sorted(p for p in (ROOT/"experiments/runtime-mihomo").rglob("*") if p.is_file() and "__pycache__" not in p.parts)
    source+=[ROOT/"experiments/managed-mihomo/run.py",ROOT/"internal/proxyengine/mihomo/testfixture/main.go",
             ROOT/"internal/proxyengine/mihomo/testfixture/counts.go",ROOT/"docs/validation/issue-14-incremental-evaluation.md"]
    def entry(path):
        data=path.read_text(encoding="utf-8").replace("\r\n","\n").encode()
        return {"path":path.relative_to(ROOT).as_posix(),"bytes":len(data),"sha256":digest(data)}
    write(DEST/"manifest.json",{"kind":"development-evaluation","date":"2026-10-13",
          "hashBasis":"git-blob (UTF-8 LF per .gitattributes)","binaryHashBasis":"actual-file-bytes",
          "evidence":[entry(p) for p in files],"source":[entry(p) for p in source],
          "redaction":"Only fixed categories, counts, loopback ports, relative timings and build identities. No private inputs, credentials, signed URLs or personal paths. Failures are retained."})
    print(json.dumps({"evidenceFiles":len(files),"manifestWritten":True}))


if __name__=="__main__":main()
