"""Check publishable Git content without printing credential values.

With no repository yet, use an isolated temporary Git index to apply .gitignore.
--staged checks index blobs, including force-added files covered by .gitignore.
"""
import argparse
from contextlib import contextmanager
import ipaddress
import json
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
SENSITIVE_NAMES = {'site-agent.json', 'agent-ui.json', 'agent.json', 'hub-state.json',
                   'pair.json', 'ui-runtime.json', 'ui-access.key'}
PRIVATE_DIRECTORIES = {'.local', '.site-agent', 'sitedeck-dis', 'open-endlesst-dis', 'preconfigured',
                       'agent-config', '__pycache__', 'node_modules'}
PRIVATE_SUFFIXES = {'.key', '.pem', '.p12', '.pfx', '.log', '.pid', '.zip', '.tgz',
                    '.tar', '.7z', '.exe', '.pyc', '.pyo'}
PAIR_TEXT = re.compile(rb'SITEPAIR1\.[A-Za-z0-9_-]{40,}')
PRIVATE_KEY = re.compile(rb'-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----')
IPV4 = re.compile(rb'(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?![\w.])')
JSON_SECRET = re.compile(rb'"(?:registrationToken|guestToken|accessToken|privateKey|password|token)"\s*:\s*"([^"\r\n]{32,})"')


def git(command, args, **kwargs):
    return subprocess.run([*command, *args], check=True, stdout=subprocess.PIPE, **kwargs).stdout


@contextmanager
def candidates(root=ROOT, staged=False):
    root = root.resolve()
    found = subprocess.run(['git', '-C', str(root), 'rev-parse', '--show-toplevel'],
                           stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    if found.returncode == 0:
        repository = Path(found.stdout.decode().strip())
        command = ['git', '-C', str(repository)]
        if staged:
            names = git(command, ['diff', '--cached', '--name-only', '--diff-filter=ACMR', '-z'])
        else:
            names = git(command, ['ls-files', '--cached', '--others', '--exclude-standard', '-z'])
        yield repository, command, sorted(set(name for name in names.decode().split('\0') if name))
    else:
        if staged:
            raise ValueError('No Git repository yet; run the check without --staged')
        with tempfile.TemporaryDirectory(prefix='site-public-index-') as temporary:
            subprocess.run(['git', 'init', '--quiet', temporary], check=True)
            command = ['git', '--git-dir', str(Path(temporary) / '.git'), '--work-tree', str(root)]
            names = git(command, ['ls-files', '--others', '--exclude-standard', '-z'])
            yield root, command, sorted(name for name in names.decode().split('\0') if name)


def private_path(name):
    p = Path(name)
    return (bool(PRIVATE_DIRECTORIES.intersection(p.parts)) or p.name in SENSITIVE_NAMES
            or p.suffix.lower() in PRIVATE_SUFFIXES or p.name.endswith(('-pair.json', '.local.json', '.tar.gz'))
            or p.name.startswith(('hub-access-', 'id_rsa', 'id_ed25519'))
            or (p.name.startswith('.env') and p.name != '.env.example')
            or name.startswith(('bin/', 'research/artifacts/', 'research/evidence/')))


def inspect(name, data, private_values=()):
    reasons = []
    if private_path(name):
        reasons.append('private runtime/build file')
    if any(value and (value in data or value in name.encode()) for value in private_values):
        reasons.append('matches a local private value')
    if PRIVATE_KEY.search(data):
        reasons.append('private key material')
    if PAIR_TEXT.search(data):
        reasons.append('complete pairing credential')
    if b'\0' not in data[:4096]:
        for match in IPV4.finditer(data):
            try:
                address = ipaddress.ip_address(match.group().decode())
            except ValueError:
                continue
            if address.is_global:
                reasons.append('literal public server IP; use private config or an example address')
                break
        for match in JSON_SECRET.finditer(data):
            value = match.group(1)
            # Deterministic test strings and explicit placeholders are public.
            if (len(set(value)) > 4 and not any(marker in value.lower() for marker in
                    [b'example', b'replace', b'placeholder', b'your_', b'your-', b'\\'])):
                reasons.append('literal credential in JSON')
                break
    return sorted(set(reasons))


def check(root, command, names, staged=False):
    private_file = root / '.local/publish-private-patterns.json'
    private_values = []
    if private_file.exists():
        values = json.loads(private_file.read_text())
        if not isinstance(values, list) or any(not isinstance(value, str) for value in values):
            raise ValueError('Private publish denylist must be a JSON string array')
        private_values = [value.encode() for value in values]
    issues = []
    for name in names:
        path = root / name
        if staged:
            mode = git(command, ['ls-files', '--stage', '--', name]).split(b' ', 1)[0]
            if mode == b'120000':
                issues.append((name, ['symbolic links require manual review']))
                continue
            data = git(command, ['show', ':' + name])
        else:
            if path.is_symlink():
                issues.append((name, ['symbolic links require manual review']))
                continue
            if not path.is_file():
                continue
            data = path.read_bytes()
        reasons = inspect(name, data, private_values)
        if reasons:
            issues.append((name, reasons))
    return issues


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--staged', action='store_true', help='check the exact blobs about to be committed')
    parser.add_argument('--root', type=Path, default=ROOT, help=argparse.SUPPRESS)
    args = parser.parse_args()
    try:
        with candidates(args.root, args.staged) as (root, command, names):
            issues = check(root, command, names, args.staged)
            for name, reasons in issues:
                print(name + ': ' + '; '.join(reasons))
            print(json.dumps({'checkedFiles': len(names), 'issues': len(issues), 'staged': args.staged}))
            return 1 if issues else 0
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        # Do not interpolate failing commands or configuration content.
        print('Publish check failed: ' + type(error).__name__)
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
