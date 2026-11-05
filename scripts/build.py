"""Build standalone Go binaries; no target C compiler or Node.js is needed."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ["linux/amd64", "linux/arm64", "linux/arm/v7", "linux/mipsle", "linux/riscv64",
           "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64"]
parser = argparse.ArgumentParser()
parser.add_argument("--targets", default=",".join(TARGETS))
parser.add_argument("--roles", default="hub,agent,cli")
parser.add_argument("--jobs", type=int, default=2)
args = parser.parse_args()

def build(job):
    target, role = job
    parts = target.split("/")
    env = dict(os.environ, CGO_ENABLED="0", GOOS=parts[0], GOARCH=parts[1])
    if len(parts) == 3:
        env["GOARM"] = parts[2].removeprefix("v")
    if parts[1] in {"mips", "mipsle"}:
        env["GOMIPS"] = "softfloat"
    name = "site-" + role + (".exe" if parts[0] == "windows" else "")
    output = ROOT / "bin" / target.replace("/", "-") / name
    output.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["go", "build", "-trimpath", ("-ldflags=-s -w -H windowsgui" if role == "agent-ui" and parts[0] == "windows" else "-ldflags=-s -w"), "-o", str(output), "./cmd/site-" + role],
                   cwd=ROOT, env=env, check=True)
    data = output.read_bytes()
    row = dict(target=target, role=role, path=str(output.relative_to(ROOT)),
               bytes=len(data), sha256=hashlib.sha256(data).hexdigest())
    print(f"built {row['path']} ({len(data)} bytes)", flush=True)
    return row

jobs = [(target, role) for target in args.targets.split(",") for role in args.roles.split(",")]
if "agent" in args.roles.split(","):
    jobs += [(target, "agent-ui") for target in args.targets.split(",") if target.startswith("windows/")]
with ThreadPoolExecutor(max_workers=args.jobs) as pool:
    results = list(pool.map(build, jobs))
(ROOT / "bin" / "manifest.json").write_text(json.dumps(results, indent=2) + "\n")
