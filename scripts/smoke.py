"""Exercise the compiled Hub, Agent, CLI and daemon as separate processes."""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import shlex
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import http.cookiejar

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--bin-dir", type=Path, default=ROOT / "bin")
args = parser.parse_args()
BIN = args.bin_dir.resolve()
processes = []
logs = []
checks = []
local_http = urllib.request.build_opener(urllib.request.ProxyHandler({}))

with tempfile.TemporaryDirectory(prefix="sitedeck-smoke-") as directory:
    work = Path(directory)

    def invoke(role, *params, expected=0):
        result = subprocess.run([str(BIN / ("site-" + role)), *map(str, params)], cwd=work,
                                capture_output=True, text=True, timeout=20)
        if result.returncode != expected:
            raise AssertionError(f"{role} {params[0] if params else ''} exited {result.returncode}: {result.stdout[:400]} {result.stderr[:400]}")
        return json.loads(result.stdout)

    def start(role, *params):
        log = (work / f"process-{len(processes)}.log").open("w+")
        logs.append(log)
        proc = subprocess.Popen([str(BIN / ("site-" + role)), *map(str, params)], cwd=work,
                                stdin=subprocess.DEVNULL, stdout=log, stderr=log)
        processes.append(proc)
        return proc

    def stop(proc):
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=8)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)

    def until(operation, seconds=15):
        end = time.monotonic() + seconds
        last = None
        while time.monotonic() < end:
            try:
                result = operation()
                if result:
                    return result
            except (AssertionError, OSError, ValueError, urllib.error.URLError) as error:
                last = error
            time.sleep(0.1)
        raise AssertionError(f"timed out: {last}")

    try:
        with socket.socket() as reserve:
            reserve.bind(("127.0.0.1", 0))
            port = reserve.getsockname()[1]
        hub_url = f"ws://127.0.0.1:{port}/ws"
        invoke("hub", "init")
        invoke("agent", "init", "--hub", hub_url)
        hub = start("hub", "serve", "--listen", f"127.0.0.1:{port}")
        until(lambda: local_http.open(f"http://127.0.0.1:{port}/healthz", timeout=1).status == 200)
        agent_args = ("run", "--hub", hub_url, "--allow", "host,ssh,serial")
        agent = start("agent", *agent_args)
        doctor = until(lambda: invoke("cli", "--transport", "relay", "doctor"))
        assert doctor["connection"]["transport"] == "relay"
        checks.append("authenticated relay")
        assert invoke("cli", "--transport", "p2p", "doctor")["connection"]["transport"] == "p2p"
        checks.append("local P2P")
        failed = invoke("cli", "--transport", "relay", "host", "exec", "--command",
                        "printf hello; printf error >&2; exit 7", expected=1)
        assert (failed["stdout"], failed["stderr"], failed["exitCode"]) == ("hello", "error", 7)
        checks.append("CLI exit code and output")
        daemon = start("cli", "--transport", "relay", "daemon", "serve", "--output", ".local/daemon.json")
        until(lambda: (work / ".local/daemon.json").is_file())
        channels = {invoke("cli", "--daemon-file", ".local/daemon.json", "doctor")["connection"]["channelId"] for _ in range(3)}
        assert len(channels) == 1
        checks.append("daemon connection reuse")
        shell = invoke("cli", "--daemon-file", ".local/daemon.json", "host", "shell", "--json", "--command", "cat")
        member = ("--daemon-file", ".local/daemon.json", "--client-id", "automation")
        invoke("cli", *member, "session", "attach", "--id", shell["id"], "--json")
        invoke("cli", *member, "session", "write", "--id", shell["id"], "--text", "automation\n")
        read = invoke("cli", *member, "session", "read", "--id", shell["id"], "--wait-ms", "1000")
        import base64
        assert b"automation" in base64.b64decode(read["data"])
        invoke("cli", *member, "session", "detach", "--id", shell["id"])
        checks.append("stable session membership across CLI processes")
        descriptor = json.loads((work / ".local/daemon.json").read_text())
        request = urllib.request.Request(descriptor["url"] + "/rpc", data=b"{}", method="POST")
        try:
            local_http.open(request, timeout=2)
            raise AssertionError("unauthenticated local IPC accepted")
        except urllib.error.HTTPError as error:
            assert error.code == 403
        checks.append("daemon IPC authentication")
        result = invoke("cli", "--daemon-file", ".local/daemon.json", "host", "exec", "--command",
                        "head -c 65536 /dev/zero | tr '\\000' x")
        captured = json.loads(Path(result["artifactPath"]).read_text())
        assert len(captured["stdout"]) == 65536
        checks.append("artifact download with SHA-256 verification")
        marker = work / "executions.txt"
        pending = subprocess.Popen([str(BIN / "site-cli"), "--daemon-file", ".local/daemon.json", "host", "exec",
                                    "--command", "printf x >> " + shlex.quote(str(marker)) + "; sleep 30"],
                                   cwd=work, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        processes.append(pending)
        until(marker.exists)
        stop(agent)
        pending.communicate(timeout=10)
        assert pending.returncode != 0
        agent = start("agent", *agent_args)
        until(lambda: invoke("cli", "--daemon-file", ".local/daemon.json", "doctor"))
        assert marker.read_text() == "x"
        checks.append("daemon reconnect without replaying a command")
        invoke("agent", "rotate")
        until(lambda: invoke("cli", "--transport", "relay", "doctor", expected=3))
        invoke("agent", "init", "--hub", hub_url, "--pair-file", ".local/new-pair.json")
        until(lambda: invoke("cli", "--profile", ".local/new-pair.json", "--transport", "relay", "doctor"))
        checks.append("credential rotation revokes old access")
        status = invoke("cli", "--daemon-file", ".local/daemon.json", "daemon", "status")
        assert isinstance(status, dict)
        invoke("cli", "--daemon-file", ".local/daemon.json", "daemon", "stop")
        daemon.wait(timeout=8)
        assert not (work / ".local/daemon.json").exists()
        checks.append("daemon shutdown removes descriptor")

        # Management uses an independent browser session; keys stay in this
        # temporary directory and are never written to the verification report.
        admin_http = urllib.request.build_opener(urllib.request.ProxyHandler({}),
                    urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        csrf = ""
        def admin_api(method, path, value=None):
            data = None if value is None else json.dumps(value).encode()
            request = urllib.request.Request(f"http://127.0.0.1:{port}/admin/api/" + path,
                                             data=data, method=method)
            if value is not None:
                request.add_header("Content-Type", "application/json")
            if csrf:
                request.add_header("X-CSRF-Token", csrf)
            with admin_http.open(request, timeout=5) as response:
                return json.load(response)
        with local_http.open(f"http://127.0.0.1:{port}/admin/", timeout=2) as page:
            assert page.status == 200 and b"SiteDeck" in page.read()
        try:
            admin_api("GET", "overview")
            raise AssertionError("management API accepted an unauthenticated request")
        except urllib.error.HTTPError as error:
            assert error.code == 401
        csrf = admin_api("POST", "login", {"secret": (work / ".local/hub-admin.key").read_text().strip()})["csrfToken"]
        checks.append("management UI and independent admin authentication")
        device_id = doctor["deviceId"]
        admin_api("PATCH", "devices/" + device_id, {"name": "CLI smoke bench"})
        assert admin_api("GET", "overview")["devices"][0]["name"] == "CLI smoke bench"
        checks.append("management device rename")
        serial_grant = admin_api("POST", "grants", {"deviceId": device_id,
                                "name": "serial test access", "scopes": ["serial"]})
        access_path = work / ".local/access.json"
        access_path.write_text(json.dumps({"deviceId": device_id, "accessToken": serial_grant["accessToken"]}))
        access_path.chmod(0o600)
        grant_args = ("--profile", ".local/new-pair.json", "--access-file", ".local/access.json")
        permissions = invoke("cli", *grant_args, "permissions", "get")
        assert permissions["serial"] and not permissions["host"]
        denied = invoke("cli", *grant_args, "host", "exec", "--command", "true", expected=1)
        assert denied["error"]["code"] == "PERMISSION_DENIED"
        assert invoke("cli", "--profile", ".local/new-pair.json", "doctor", expected=3)["error"]["code"] == "ACCESS_REQUIRED"
        checks.append("scoped grant file and enforced grant-only policy")
        host_grant = admin_api("POST", "grants", {"deviceId": device_id,
                              "name": "connection management test", "scopes": ["host"]})
        host_access = work / ".local/host-access.json"
        host_access.write_text(json.dumps({"deviceId": device_id, "accessToken": host_grant["accessToken"]}))
        host_access.chmod(0o600)
        daemon2 = start("cli", "--profile", ".local/new-pair.json", "--access-file", ".local/host-access.json",
                        "--transport", "p2p", "daemon", "serve", "--output", ".local/daemon.json")
        until(lambda: (work / ".local/daemon.json").is_file())
        connection = until(lambda: invoke("cli", "--daemon-file", ".local/daemon.json", "doctor"))["connection"]
        current_id = connection["channelId"]
        until(lambda: any(c["id"] == current_id and c["transport"] == "p2p" and c.get("authenticatedAt")
                          for c in admin_api("GET", "overview")["connections"]))
        admin_api("DELETE", "connections/" + current_id)
        until(lambda: all(c["id"] != current_id for c in admin_api("GET", "overview")["connections"]))
        checks.append("management observes and disconnects real P2P channels")
        admin_api("DELETE", "grants/" + host_grant["grant"]["id"])
        denied = invoke("cli", "--profile", ".local/new-pair.json", "--access-file", ".local/host-access.json", "doctor", expected=3)
        assert denied["error"]["code"] == "ACCESS_DENIED"
        invoke("cli", "--daemon-file", ".local/daemon.json", "daemon", "stop")
        daemon2.wait(timeout=8)
        admin_api("DELETE", "grants/" + serial_grant["grant"]["id"])
        assert invoke("cli", *grant_args, "doctor", expected=3)["error"]["code"] == "ACCESS_DENIED"
        checks.append("management revocation rejects subsequent access")
        identity_file = work / ".local/agent.json"
        identity = json.loads(identity_file.read_text())
        identity["guestExpiresAt"] = "2000-01-01T00:00:00Z"
        identity_file.write_text(json.dumps(identity))
        agent.wait(timeout=8)
        assert agent.returncode != 0
        checks.append("expired guest access closes Agent and active channels")
    except Exception:
        for index, log in enumerate(logs):
            log.flush()
            log.seek(0)
            print(f"process {index}: {log.read()[-2500:]}")
        raise
    finally:
        for proc in reversed(processes):
            stop(proc)
        for log in logs:
            log.close()

report = dict(time=datetime.now(timezone.utc).isoformat(), checks=checks, passed=len(checks))
print(json.dumps(report, indent=2))
(ROOT / "bin" / "smoke-result.json").write_text(json.dumps(report, indent=2) + "\n")
