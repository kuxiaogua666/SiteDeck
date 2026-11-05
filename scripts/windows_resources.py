"""Regenerate architecture-scoped Windows icons and version resources.

Requires Go only; generated .syso files are kept in cmd/ so ordinary builds and
non-Windows/headless builds need no resource compiler or image dependencies.
The original vector is internal/agentui/web/icon.svg; app.ico contains 7 sizes.
"""
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
version = re.search(r'const Version = "([^"]+)"', (ROOT / 'internal/protocol/protocol.go').read_text()).group(1)
for role in ['hub', 'agent', 'agent-ui', 'cli']:
    name = 'site-' + role
    resource = {
        'RT_GROUP_ICON': {'#1': {'0000': 'app.ico'}},
        'RT_MANIFEST': {'#1': {'0409': {'minimum-os': 'win10', 'execution-level': 'as invoker', 'dpi-awareness': 'per monitor v2', 'long-path-aware': True}}},
        'RT_VERSION': {'#1': {'0000': {
            'fixed': {'file_version': version + '.0', 'product_version': version + '.0'},
            'info': {'0409': {'FileDescription': 'SiteDeck ' + role, 'FileVersion': version, 'ProductName': 'SiteDeck', 'ProductVersion': version, 'OriginalFilename': name + '.exe'}}
        }}}
    }
    with tempfile.TemporaryDirectory(prefix='site-winres-') as tmp:
        shutil.copy2(ROOT / 'deploy/icons/app.ico', Path(tmp) / 'app.ico')
        config = Path(tmp) / 'winres.json'
        config.write_text(json.dumps(resource))
        subprocess.run(['go', 'run', 'github.com/tc-hib/go-winres@v0.3.3', 'make', '--in', str(config), '--arch', 'amd64,arm64', '--out', str(ROOT / 'cmd' / name / 'rsrc')], cwd=ROOT, check=True)
    print('Embedded Windows resources:', name, flush=True)
