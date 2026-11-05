"""Upload a prepared Hub release to the server selected in private settings.

The SSH password is prompted, passed on an anonymous pipe and never persisted.
"""
import argparse
import getpass
import os
from pathlib import Path
import subprocess
import tempfile
import sys
import socket
import json
import urllib.request
import time
from deployment_config import add_arguments, load

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--archive", type=Path)
parser.add_argument("--download-admin", action="store_true")
parser.add_argument("--start-admin-tunnel", action="store_true")
parser.add_argument("--verify", action="store_true")
add_arguments(parser)
args = parser.parse_args()
try:
    server = load(args)
except (ValueError, OSError) as error:
    parser.error(str(error))
verify_command = [sys.executable, str(ROOT/"scripts/verify_public_hub.py"), "--server-config", str(args.server_config), "--host", server["host"]]
if args.verify and not args.archive and not args.download_admin:
    subprocess.run(verify_command,check=True)
    raise SystemExit(0)
download_path = None
if args.download_admin:
    destination = server["stateDirectory"]
    destination.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(destination, 0o700)
    fd, temporary = tempfile.mkstemp(prefix=".admin-download-", dir=destination)
    os.close(fd)
    download_path = Path(temporary)
    transfers = [server["scpTarget"] + ":" + server["remoteRoot"] + "/secrets/hub-admin.key", str(download_path)]
else:
    if not args.archive:
        raise SystemExit("Provide --archive or --download-admin")
    archive = args.archive.resolve()
    if not archive.is_relative_to(ROOT / ".local") or not archive.is_file():
        raise SystemExit("The release archive must be inside this project's private .local directory")
    transfers = [str(archive), server["scpTarget"] + ":" + server["remoteRoot"] + "/incoming/hub-release.tar.gz"]
password = getpass.getpass("SSH password: ")
read_fd, write_fd = os.pipe()
try:
    os.write(write_fd, (password + "\n").encode())
    os.close(write_fd)
    write_fd = -1
    command = ["sshpass", "-d", str(read_fd), "scp", "-o", "StrictHostKeyChecking=yes", "-o",
               "UserKnownHostsFile=" + str(server["knownHostsFile"]),
               "-o", "ConnectTimeout=15", *transfers]
    result = subprocess.run(command, pass_fds=(read_fd,), timeout=180)
    if result.returncode == 0 and download_path is not None:
        if len(download_path.read_bytes().strip()) < 32:
            raise SystemExit("Downloaded management credential is invalid")
        os.chmod(download_path, 0o600)
        download_path.replace(destination / "hub-admin.key")
        print("Management credential saved in " + str(destination / "hub-admin.key"))
    if result.returncode == 0 and args.start_admin_tunnel:
        if not args.download_admin:
            raise SystemExit("--start-admin-tunnel requires --download-admin")
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 48764))
        tunnel_read, tunnel_write = os.pipe()
        try:
            os.write(tunnel_write, (password+"\n").encode());os.close(tunnel_write);tunnel_write=-1
            log_path = destination / "ssh-tunnel.log"
            fd = os.open(log_path, os.O_CREAT|os.O_APPEND|os.O_WRONLY,0o600)
            with os.fdopen(fd,"ab") as log:
                tunnel = subprocess.Popen(["sshpass","-d",str(tunnel_read),"ssh","-N","-o","StrictHostKeyChecking=yes",
                    "-o","UserKnownHostsFile="+str(server["knownHostsFile"]),"-o","ConnectTimeout=15",
                    "-o","ExitOnForwardFailure=yes","-o","ServerAliveInterval=20","-o","ServerAliveCountMax=3",
                    "-L","127.0.0.1:48764:127.0.0.1:48765",server["sshTarget"]],
                    pass_fds=(tunnel_read,),stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=log,start_new_session=True)
        finally:
            os.close(tunnel_read)
            if tunnel_write>=0:os.close(tunnel_write)
        http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        for attempt in range(40):
            try:
                with http.open("http://127.0.0.1:48764/healthz",timeout=2) as response:
                    assert json.load(response)["status"]=="ok"
                break
            except OSError:
                if tunnel.poll() is not None:
                    raise SystemExit("SSH tunnel failed; inspect "+str(log_path))
                time.sleep(.5)
        else:
            tunnel.terminate()
            raise SystemExit("SSH tunnel did not become ready")
        pid_path=destination/"ssh-tunnel.pid"
        pid_path.write_text(str(tunnel.pid)+"\n");pid_path.chmod(0o600)
        print("Private management tunnel ready: http://127.0.0.1:48764/admin/")
    if result.returncode == 0 and args.verify:
        subprocess.run(verify_command,check=True)
    raise SystemExit(result.returncode)
finally:
    os.close(read_fd)
    if write_fd >= 0:
        os.close(write_fd)
    if download_path is not None:
        download_path.unlink(missing_ok=True)
