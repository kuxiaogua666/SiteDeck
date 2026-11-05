"""Read private server selection without placing endpoints in source files."""
import ipaddress
import json
import os
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]


def add_arguments(parser):
    parser.add_argument('--server-config', type=Path, default=ROOT / '.local/server-deploy.json')
    parser.add_argument('--host', help='Hub server IP or hostname; otherwise private config or SITEDECK_SERVER_HOST')


def load(args):
    values = {}
    if args.server_config.exists():
        values = json.loads(args.server_config.read_text())
        if not isinstance(values, dict):
            raise ValueError('Server config must be a JSON object')
    host = args.host or os.environ.get('SITEDECK_SERVER_HOST') or os.environ.get('ENDLESS_SERVER_HOST') or values.get('host', '')
    if not isinstance(host, str) or not host or len(host) > 253:
        raise ValueError('Provide --host, SITEDECK_SERVER_HOST, or .local/server-deploy.json; see deploy/server-deploy.example.json')
    try:
        ipaddress.ip_address(host)
    except ValueError:
        if not re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?', host):
            raise ValueError('Invalid server hostname')
    user = values.get('user', 'root')
    remote_root = values.get('remoteRoot', '/root/sitedeck')
    if not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_-]*', user):
        raise ValueError('Invalid SSH username')
    if not re.fullmatch(r'/[A-Za-z0-9_./-]+', remote_root) or '..' in remote_root.split('/'):
        raise ValueError('Invalid remote deployment path')
    def local_path(key, default):
        path = Path(values.get(key, default)).expanduser()
        return path if path.is_absolute() else ROOT / path
    return dict(host=host, sshTarget=user + '@' + host,
                scpTarget=user + '@' + ('[' + host + ']' if ':' in host else host),
                authority=('[' + host + ']' if ':' in host else host) + ':48763',
                remoteRoot=remote_root.rstrip('/'),
                stateDirectory=local_path('stateDirectory', '.local/public-hub'),
                registrationFile=local_path('registrationFile', '../sitedeck-dis/.local/hub.key'),
                knownHostsFile=local_path('knownHostsFile', '.local/remote-deploy-known-hosts'),
                deviceId=values.get('deviceId', ''))
