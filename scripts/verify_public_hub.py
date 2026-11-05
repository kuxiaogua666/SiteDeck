"""Verify the deployed public gateway with real local Agent/CLI processes."""
from datetime import datetime, timezone
import argparse
from deployment_config import add_arguments, load
import json
import os
from pathlib import Path
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from http.cookiejar import CookieJar

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
add_arguments(parser)
args = parser.parse_args()
try:
    server = load(args)
except (ValueError, OSError) as error:
    parser.error(str(error))
HTTPS = "https://" + server["authority"]
WSS = "wss://" + server["authority"] + "/ws"
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
checks = []
for path, expected in [("/",404),("/admin/",404),("/admin/api/login",404),("/healthz",404),("/ws",426)]:
    request = urllib.request.Request(HTTPS + path)
    try:
        response = http.open(request, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        assert response.status == expected, (path, response.status)
checks += ["public IP TLS certificate trusted without bypass", "public management and health endpoints inaccessible", "WebSocket endpoint requires an upgrade"]

env = {key: value for key,value in os.environ.items() if key.upper() not in {"HTTP_PROXY","HTTPS_PROXY","ALL_PROXY","NO_PROXY"}}
env["NO_PROXY"] = "*"
agent = ROOT / "bin/site-agent"
cli = ROOT / "bin/site-cli"
registration = server["registrationFile"]
with tempfile.TemporaryDirectory(prefix="sitedeck-public-hub-") as temporary:
    work = Path(temporary)
    def invoke(binary, *args, expected=0):
        process = subprocess.run([str(binary), *map(str,args)], cwd=work, env=env,
                                 capture_output=True, text=True, timeout=40)
        if process.returncode != expected:
            raise RuntimeError("Public Hub verification failed: " + process.stdout[:400] + process.stderr[:400])
        return json.loads(process.stdout)

    identity = invoke(agent, "init", "--hub", WSS, "--guest-ttl", "10m", "--name", "Deployment self-test (offline after verification)")
    with (work / "agent.log").open("w") as log:
        process = subprocess.Popen([str(agent), "run", "--hub", WSS, "--registration-file", str(registration), "--allow", "host"],
                                   cwd=work, env=env, stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 40
            while True:
                try:
                    doctor = invoke(cli, "--transport", "relay", "doctor")
                    break
                except RuntimeError:
                    if process.poll() is not None or time.monotonic() > deadline:
                        raise
                    time.sleep(1)
            assert doctor["connection"]["transport"] == "relay"
            checks.append("Agent registration and authenticated relay through public WSS")
            result = invoke(cli, "--transport", "relay", "host", "exec", "--command", "printf SITEDECK_PUBLIC_RELAY_OK")
            assert result["stdout"] == "SITEDECK_PUBLIC_RELAY_OK" and result["exitCode"] == 0
            checks.append("end-to-end encrypted command and output through overseas relay")
            profile = json.loads((work / ".local/pair.json").read_text())
            profile["token"] = "0" * 64
            bad = work / "wrong-secret.json"
            bad.write_text(json.dumps(profile));bad.chmod(0o600)
            result = invoke(cli, "--profile", bad, "--transport", "relay", "doctor", expected=3)
            assert result["error"]["code"] == "CHANNEL_AUTH_FAILED"
            checks.append("wrong guest secret rejected")
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill();process.wait()

private_admin = server["stateDirectory"] / "hub-admin.key"
private_ready = False
windows_online = False
if private_admin.exists():
    admin_http = urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(CookieJar()))
    request = urllib.request.Request("http://127.0.0.1:48764/admin/api/login",
        data=json.dumps({"secret":private_admin.read_text().strip()}).encode(),headers={"Content-Type":"application/json"})
    with admin_http.open(request,timeout=10) as response:
        assert json.load(response)["authenticated"]
    with admin_http.open("http://127.0.0.1:48764/admin/api/overview",timeout=10) as response:
        overview = json.load(response)
    private_ready = True
    windows_online = any(device.get("id")==server["deviceId"] and device.get("online") for device in overview["devices"])
    checks.append("private SSH management tunnel and administrator authentication")

result = {"time":datetime.now(timezone.utc).isoformat(),"endpoint":WSS,"passed":len(checks),
          "checks":checks,"temporaryTestDevice":identity["deviceId"],"testDeviceStopped":True}
result["privateManagementReady"] = private_ready
result["windowsDeviceOnline"] = windows_online
destination = server["stateDirectory"] / "verification.json"
destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
destination.write_text(json.dumps(result, indent=2)+"\n")
destination.chmod(0o600)
print(json.dumps(result, indent=2))
