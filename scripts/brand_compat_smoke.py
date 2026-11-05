"""Check SiteDeck against locally preserved pre-rename binaries, without a server."""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--legacy-bin', type=Path, default=ROOT / '.local/brand-compat')
args = parser.parse_args()
old = {role: args.legacy_bin / ('site-' + role + '-0.5.0') for role in ['hub', 'agent', 'cli']}
new = {role: ROOT / 'bin' / ('site-' + role) for role in ['hub', 'agent', 'cli']}
for path in [*old.values(), *new.values()]:
    if not path.is_file():
        raise SystemExit('Missing compatibility baseline or current binary')
env = {key: value for key, value in os.environ.items() if key.upper() not in {'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'NO_PROXY'}}
env['NO_PROXY'] = '*'
http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
checks = []
for label, hub_bin, agent_bin, cli_bin in [
    ('new CLI with legacy Agent', new['hub'], old['agent'], new['cli']),
    ('legacy CLI with new Agent', new['hub'], new['agent'], old['cli']),
    ('new Agent and CLI with legacy Hub', old['hub'], new['agent'], new['cli'])
]:
    with tempfile.TemporaryDirectory(prefix='sitedeck-brand-compat-') as temporary:
        work = Path(temporary)
        config = work / 'config'
        case_env = dict(env, XDG_CONFIG_HOME=str(config))
        processes = []
        with (work / 'process.log').open('wb') as log:
            def invoke(binary, *arguments):
                result = subprocess.run([str(binary), *map(str, arguments)], cwd=work, env=case_env, capture_output=True, timeout=15)
                if result.returncode:
                    return result.returncode, None
                return 0, json.loads(result.stdout)
            def start(binary, *arguments):
                p = subprocess.Popen([str(binary), *map(str, arguments)], cwd=work, env=case_env, stdin=subprocess.DEVNULL, stdout=log, stderr=log)
                processes.append(p)
                return p
            try:
                with socket.socket() as probe:
                    probe.bind(('127.0.0.1', 0)); port = probe.getsockname()[1]
                hub_url = f'ws://127.0.0.1:{port}/ws'
                registration = work / 'hub.key'
                code, _ = invoke(hub_bin, 'init', '--registration-file', registration, '--admin-file', work / 'admin.key', '--state', work / 'hub-state.json')
                assert code == 0, label + ': Hub init'
                start(hub_bin, 'serve', '--listen', f'127.0.0.1:{port}', '--registration-file', registration, '--admin-file', work / 'admin.key', '--state', work / 'hub-state.json')
                deadline = time.monotonic() + 10
                while True:
                    try:
                        with http.open(f'http://127.0.0.1:{port}/healthz', timeout=1) as response:
                            assert response.status == 200
                        break
                    except OSError:
                        if time.monotonic() > deadline: raise RuntimeError(label + ': Hub not ready')
                        time.sleep(.05)
                state = work / 'agent.json'; profile = work / 'pair.json'
                code, _ = invoke(agent_bin, 'init', '--state', state, '--hub', hub_url, '--pair-file', profile, '--guest-ttl', '10m')
                assert code == 0, label + ': Agent init'
                paired = profile.read_bytes()
                start(agent_bin, 'run', '--state', state, '--hub', hub_url, '--registration-file', registration, '--allow', 'host')
                deadline = time.monotonic() + 10
                while True:
                    code, result = invoke(cli_bin, '--profile', profile, '--transport', 'relay', 'doctor')
                    if code == 0: break
                    if time.monotonic() > deadline: raise RuntimeError(label + ': authentication failed')
                    time.sleep(.1)
                assert result['authenticated'], label
                code, result = invoke(cli_bin, '--profile', profile, '--transport', 'relay', 'host', 'exec', '--command', 'printf SITEDECK_COMPAT_OK')
                assert code == 0 and result['stdout'] == 'SITEDECK_COMPAT_OK', label
                assert profile.read_bytes() == paired, 'Pairing changed during compatibility check'
                checks.append(label)
            finally:
                for p in reversed(processes):
                    if p.poll() is None: p.terminate()
                for p in reversed(processes):
                    try: p.wait(timeout=10)
                    except subprocess.TimeoutExpired: p.kill(); p.wait()
result = {'passed':len(checks), 'checks':checks, 'pairingProfilesUnchanged':True}
(ROOT / 'bin/brand-compat-smoke-result.json').write_text(json.dumps(result, indent=2)+'\n')
print(json.dumps(result, indent=2))
