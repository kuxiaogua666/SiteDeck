"""Run the downloaded REA CLI against selected packaged Terminal modules.

Requires a built ../rea and the unpacked app.asar. Evidence is never overwritten.
"""
import argparse
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--asar", type=Path, default=ROOT / "research/artifacts/windows-app/resources/app.asar")
parser.add_argument("--output", type=Path, required=True, help="new Evidence bundle path")
args = parser.parse_args()
sources = [
    "dist/common/device/protocol.js", "dist/common/hub/p2p.js",
    "dist/common/hub/protocol.js", "dist/common/hub/guest.js",
    "dist/terminal/agent/gateway.js", "dist/terminal/agent/manager.js",
    "dist/terminal/serial/ring-buffer.js", "dist/terminal/serial/session.js",
    "dist/terminal/services/host-service.js", "dist/terminal/services/session-broker.js",
    "dist/terminal/services/ssh-service.js", "dist/terminal/services/serial-service.js",
    "dist/terminal/services/rpc-gateway.js",
]
command = ["node", str(ROOT.parent / "rea/scripts/rea.mjs"),
           "analyze-javascript-application", str(args.asar.resolve())]
for source in sources:
    command.extend(["--source-path", source])
command.extend(["--detail", "summary", "--evidence_output", str(args.output.resolve()), "--json"])
subprocess.run(command, check=True)
