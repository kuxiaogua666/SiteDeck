"""Build private, preconfigured Agent-only packages without copying identity."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
import zipfile

ROOT=Path(__file__).resolve().parents[1]
parser=argparse.ArgumentParser()
parser.add_argument("--config",required=True,type=Path,help="private portable settings to include")
parser.add_argument("--output",type=Path,default=ROOT.parent/"sitedeck-dis/preconfigured")
args=parser.parse_args()
settings=json.loads(args.config.read_text())
allowed={"version","hub","registrationToken","remoteEnabled","permissions","autoStart","guestTTLHours","iceFile","name"}
if set(settings)-allowed or not settings.get("hub") or len(settings.get("registrationToken",""))<32:
    raise SystemExit("Expected deployment settings only; device identity must not be packaged")
version=re.search(r'const Version = "([^"]+)"',(ROOT/"internal/protocol/protocol.go").read_text()).group(1)
args.output.mkdir(parents=True,exist_ok=True,mode=0o700);os.chmod(args.output,0o700)
rows=[]
for target in sorted((ROOT/"bin").iterdir()):
    if not target.is_dir():continue
    binary=target/("site-agent.exe" if target.name.startswith("windows-") else "site-agent")
    if not binary.is_file():continue
    name=f"sitedeck-{version}-agent-{target.name}.zip"
    archive=args.output/name
    fd,temporary=tempfile.mkstemp(prefix=".agent-",dir=args.output);os.close(fd)
    try:
        with zipfile.ZipFile(temporary,"w",compression=zipfile.ZIP_DEFLATED) as output:
            prefix="agent-"+target.name+"/"
            output.write(binary,prefix+binary.name)
            item=zipfile.ZipInfo(prefix+"site-agent.json");item.create_system=3;item.external_attr=0o100600<<16;item.compress_type=zipfile.ZIP_DEFLATED
            output.writestr(item,json.dumps(settings,ensure_ascii=False,indent=2)+"\n")
            output.write(ROOT/"docs/easy-deployment.md",prefix+"README.md")
            output.write(ROOT/"deploy/THIRD_PARTY_UI_NOTICES.txt",prefix+"THIRD_PARTY_UI_NOTICES.txt")
        os.chmod(temporary,0o600);os.replace(temporary,archive)
    finally:Path(temporary).unlink(missing_ok=True)
    rows.append({"target":target.name,"archive":name,"bytes":archive.stat().st_size,"sha256":hashlib.sha256(archive.read_bytes()).hexdigest()})
(args.output/"manifest.json").write_text(json.dumps(rows,indent=2)+"\n");os.chmod(args.output/"manifest.json",0o600)
print(json.dumps({"privatePackages":len(rows),"directory":str(args.output.resolve()),"containsRegistrationCredential":True}))
