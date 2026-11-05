"""Install this release on the explicitly selected Ubuntu server as root."""
import argparse
import ipaddress
import re
import grp
import hashlib
import json
import os
from pathlib import Path
import pwd
import shutil
import subprocess
import tempfile
import time
import urllib.request

RELEASE = Path(__file__).resolve().parent
# An uploaded release lives in <existing installation>/incoming/<release>.
# Reuse that root and its service IDs when upgrading a previous installation.
ROOT = RELEASE.parent.parent if RELEASE.parent.name == "incoming" else Path("/root/sitedeck")
SERVICE_PREFIX = "sitedeck"
legacy_unit = Path("/etc/systemd/system/endless-hub.service")
if legacy_unit.exists() and (ROOT / "state/hub-state.json").exists():
    if "RootDirectory=" + str(ROOT) + "\n" in legacy_unit.read_text():
        SERVICE_PREFIX = "endless"
ACCOUNT = SERVICE_PREFIX + "-hub"
UNITS = [SERVICE_PREFIX + suffix for suffix in ["-hub.service", "-gateway.service", "-cert-renew.service", "-cert-renew.timer"]]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--host", help="Hub certificate IP or hostname; persisted privately on the server")
args = parser.parse_args()
host_file = ROOT / "secrets/server-host"
host = args.host or os.environ.get("SITEDECK_SERVER_HOST")
if not host and host_file.exists():
    host = host_file.read_text().strip()
if not host:
    parser.error("Provide --host or SITEDECK_SERVER_HOST for the first installation")
try:
    ipaddress.ip_address(host)
except ValueError:
    if not re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?", host) or len(host) > 253:
        parser.error("Invalid certificate IP or hostname")
if os.geteuid() != 0 or not str(RELEASE).startswith(str(ROOT / "incoming") + "/"):
    raise SystemExit("Run the uploaded installer as root from /root/sitedeck/incoming/")
manifest = json.loads((RELEASE / "release.json").read_text())
for name, expected in manifest["sha256"].items():
    path = RELEASE / name
    if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != expected:
        raise SystemExit("Release checksum mismatch: " + name)
registration = (RELEASE / "registration.key").read_bytes()
if len(registration.strip()) < 32:
    raise SystemExit("Invalid Agent registration credential")
existing = ROOT / "secrets/hub.key"
if existing.exists() and existing.read_bytes().strip() != registration.strip():
    raise SystemExit("Refusing to replace an existing different Hub registration credential")

def run(*args):
    subprocess.run(args, check=True)

try:
    pwd.getpwnam(ACCOUNT)
except KeyError:
    run("useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", ACCOUNT)
account = pwd.getpwnam(ACCOUNT)
group = grp.getgrnam(ACCOUNT)
for name in ["bin", "secrets", "state", "nginx", "scripts", "logs", "tls", "backups"]:
    (ROOT / name).mkdir(exist_ok=True, mode=0o700)
os.chmod(ROOT / "bin", 0o755)
os.chown(ROOT / "secrets", 0, group.gr_gid)
os.chmod(ROOT / "secrets", 0o750)
os.chown(ROOT / "state", account.pw_uid, group.gr_gid)
os.chmod(ROOT / "state", 0o700)

def install(source, destination, mode=0o644):
    destination.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix=".install-", dir=destination.parent)
    try:
        with os.fdopen(fd, "wb") as output, source.open("rb") as incoming:
            if source.name in {"nginx.conf", "publish-certificate.sh", "renew-certificate.sh", "verify-certificate.py", "logrotate.conf"} or source.suffix in {".service", ".timer"}:
                content = incoming.read().replace(b"__HUB_HOST__", host.encode("ascii"))
                content = content.replace(b"/root/sitedeck", str(ROOT).encode())
                for component in ["hub", "gateway", "cert-renew"]:
                    content = content.replace(("sitedeck-" + component).encode(), (SERVICE_PREFIX + "-" + component).encode())
                output.write(content)
            else:
                shutil.copyfileobj(incoming, output)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(name, mode)
        os.replace(name, destination)
    finally:
        Path(name).unlink(missing_ok=True)

install(RELEASE / "site-hub", ROOT / "bin/site-hub", 0o755)
if not existing.exists():
    install(RELEASE / "registration.key", existing, 0o640)
os.chown(existing, 0, group.gr_gid)
os.chmod(existing, 0o640)
run(str(ROOT / "bin/site-hub"), "init", "--registration-file", str(existing),
    "--admin-file", str(ROOT / "secrets/hub-admin.key"), "--state", str(ROOT / "state/hub-state.json"))
os.chown(ROOT / "secrets/hub-admin.key", 0, group.gr_gid)
os.chmod(ROOT / "secrets/hub-admin.key", 0o640)
for path in (ROOT / "state").iterdir():
    if path.is_file():
        os.chown(path, account.pw_uid, group.gr_gid)
        os.chmod(path, 0o600)

# Keep deployment selection outside the public source and release templates.
fd = os.open(host_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as output:
    output.write(host + "\n")
os.chmod(host_file, 0o600)
prefix_file = ROOT / "secrets/service-prefix"
fd = os.open(prefix_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as output:
    output.write(SERVICE_PREFIX + "\n")
os.chmod(prefix_file, 0o600)
install(RELEASE / "nginx.conf", ROOT / "nginx/nginx.conf")
install(RELEASE / "publish-certificate.sh", ROOT / "scripts/publish-certificate.sh", 0o700)
install(RELEASE / "renew-certificate.sh", ROOT / "scripts/renew-certificate.sh", 0o700)
install(RELEASE / "verify-certificate.py", ROOT / "scripts/verify-certificate.py", 0o600)
install(RELEASE / "logrotate.conf", Path("/etc/logrotate.d") / ACCOUNT)
for name in UNITS:
    template_name = "sitedeck" + name[len(SERVICE_PREFIX):]
    destination = Path("/etc/systemd/system") / name
    if destination.exists() and destination.read_bytes() != (RELEASE / template_name).read_bytes():
        shutil.copy2(destination, ROOT / "backups" / (name + "." + str(time.time_ns())))
    install(RELEASE / template_name, destination)
run(str(ROOT / "scripts/publish-certificate.sh"))
run("systemctl", "daemon-reload")
# Ubuntu 22.04's analyzer checks ExecStart against the host filesystem even
# when RootDirectory is set. Check unit loading, then exercise the real chroot.
for name in UNITS[:3]:
    load_state = subprocess.check_output(["systemctl", "show", name, "--property=LoadState", "--value"], text=True).strip()
    if load_state != "loaded":
        raise SystemExit("Systemd could not load " + name + ": " + load_state)
run("systemctl", "enable", "--now", SERVICE_PREFIX + "-hub.service")
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for attempt in range(30):
    try:
        with http.open("http://127.0.0.1:48765/healthz", timeout=2) as response:
            if json.load(response)["status"] == "ok":
                break
    except (OSError, ValueError):
        time.sleep(.5)
else:
    raise SystemExit("Hub backend did not become healthy; inspect journalctl -u sitedeck-hub")
run("systemctl", "enable", "--now", SERVICE_PREFIX + "-gateway.service")
run("systemctl", "enable", "--now", SERVICE_PREFIX + "-cert-renew.timer")
install(RELEASE / "release.json", ROOT / "release.json")
print(json.dumps({"installed": True, "directory": str(ROOT), "endpoint": "wss://" + ("[" + host + "]" if ":" in host else host) + ":48763/ws", "backend": "127.0.0.1:48765", "management": "SSH tunnel only", "certificateRenewal": SERVICE_PREFIX + "-cert-renew.timer"}))
