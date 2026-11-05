"""Exercise the compiled Agent UI and its shared CLI identity in real processes."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
AGENT = ROOT / "bin/site-agent"
checks = []
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))

with tempfile.TemporaryDirectory(prefix="sitedeck-ui-") as temporary:
    directory = Path(temporary)
    process = subprocess.Popen([str(AGENT), "ui", "--no-open", "--listen", "127.0.0.1:0",
                                "--data-dir", temporary], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        ready = json.loads(process.stdout.readline())
        endpoint = ready["url"]
        credential = Path(ready["accessFile"]).read_text().strip()
        auth = {}

        def call(path, body=None, expected=200, origin=None, csrf=True, authenticated=True):
            headers = {"Content-Type": "application/json"}
            if authenticated and auth:
                headers["Authorization"] = "Bearer " + auth["token"]
                if csrf:
                    headers["X-CSRF-Token"] = auth["csrfToken"]
            if origin:
                headers["Origin"] = origin
            request = urllib.request.Request(endpoint + path,
                data=None if body is None else json.dumps(body).encode(), headers=headers)
            try:
                response = http.open(request, timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                data = json.load(response)
                assert response.status == expected, (path, response.status, data.get("error"))
                return data

        call("/api/status", expected=401, authenticated=False)
        auth = call("/api/login", {"token": credential})
        checks.append("UI requires a per-process local credential")
        call("/api/start", {}, expected=403, csrf=False)
        call("/api/start", {}, expected=403, origin="http://127.0.0.1:9999")
        checks.append("cross-origin and missing CSRF requests rejected")
        settings = dict(version=1, hub="ws://127.0.0.1:1/ws", name="UI Smoke Device", remoteEnabled=False,
                        permissions=[], guestTTLHours=24, autoStart=False,
                        registrationToken="a" * 64)
        call("/api/config", {"settings": settings, "restart": True})
        call("/api/start", {})

        def rpc(method, params):
            return call("/api/rpc", {"id": "ui-smoke", "method": method, "params": params, "timeoutMs": 5000})

        denied = rpc("host.exec", {"command": "printf forbidden"})
        assert denied["error"]["code"] == "PERMISSION_DENIED"
        checks.append("local terminal disabled until explicitly permitted")
        settings["permissions"] = ["host"]
        settings["registrationToken"] = ""
        call("/api/config", {"settings": settings, "restart": True})
        result = rpc("host.exec", {"command": "printf UI_PROCESS_OK"})
        assert result["result"]["stdout"] == "UI_PROCESS_OK"
        checks.append("save and restart applies local capabilities")
        session = rpc("host.shell", {"shell": "/bin/sh", "rows": 24, "cols": 80})["result"]
        sid = session["id"]
        rpc("session.attach", {"id": sid})
        rpc("session.write", {"id": sid, "text": "printf 'UI_PTY_%s\\n' OK\n"})
        import base64
        cursor, output = "", b""
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            result = rpc("session.read", {"id": sid, "cursor": cursor, "waitMs": 500})["result"]
            cursor = result["nextCursor"]
            output += base64.b64decode(result["data"] or "")
            if b"UI_PTY_OK" in output:
                break
        assert b"UI_PTY_OK" in output
        rpc("session.resize", {"id": sid, "rows": 30, "cols": 120})
        rpc("session.detach", {"id": sid})
        checks.append("real PTY input, output, resize and detach")
        config = directory / "agent-ui.json"
        duplicate = subprocess.run([str(AGENT), "run", "--config", str(config)], capture_output=True, timeout=5)
        assert duplicate.returncode != 0
        checks.append("GUI and CLI cannot run duplicate identity owners")
        pair_before = call("/api/pair")
        call("/api/renew", {})
        pair_after = call("/api/pair")
        assert pair_before["token"] != pair_after["token"]
        assert pair_before["deviceId"] == pair_after["deviceId"]
        checks.append("pair export and credential rotation preserve device identity")
        status = json.dumps(call("/api/status"))
        assert "a" * 64 not in status and pair_after["token"] not in status
        checks.append("status and event log do not disclose credentials")
        call("/api/stop", {})
        assert rpc("host.exec", {"command": "true"})["error"]["code"] == "SERVICE_STOPPED"
        call("/api/shutdown", {})
        assert process.wait(timeout=10) == 0
        assert not (directory / "ui-runtime.json").exists()
        assert not (directory / "ui-access.key").exists()
        checks.append("stop and exit release service and runtime credential files")
        headless = subprocess.Popen([str(AGENT), "run", "--config", str(config)], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            time.sleep(.25)
            assert headless.poll() is None
            headless.terminate()
            assert headless.wait(timeout=10) == 0
        finally:
            if headless.poll() is None:
                headless.kill()
                headless.wait()
        checks.append("CLI runs headlessly from the saved GUI configuration")
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=10)

result = {"passed": len(checks), "checks": checks}
(ROOT / "bin/ui-smoke-result.json").write_text(json.dumps(result, indent=2) + "\n")
print(json.dumps(result, indent=2))
