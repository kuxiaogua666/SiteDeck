"""Offline regression tests for source publication and private deployment config."""
import argparse
import ast
import shutil
import base64
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from check_public_repo import candidates, check, inspect
from deployment_config import load


class PublicationTests(unittest.TestCase):
    def test_scanner_distinguishes_templates_from_credentials(self):
        self.assertFalse(inspect('deploy/site-agent.example.json', b'{"registrationToken":""}'))
        self.assertFalse(inspect('example.md', b'http://127.0.0.1:48763 https://192.0.2.10'))
        public_ip = str(ipaddress.ip_address(0x08080808)).encode()
        self.assertTrue(inspect('example.md', b'https://' + public_ip))
        token = base64.b64encode(bytes(range(32)))
        self.assertTrue(inspect('config.json', b'{"registrationToken":"' + token + b'"}'))
        self.assertTrue(inspect('notes.md', b'SITEPAIR1.' + base64.urlsafe_b64encode(bytes(range(64)))))
        self.assertTrue(inspect('notes.md', b'private-host', [b'private-host']))

    def test_ignore_rules_and_force_added_staged_blob(self):
        with tempfile.TemporaryDirectory(prefix='site-public-test-') as tmp:
            root = Path(tmp)
            subprocess.run(['git', 'init', '--quiet', tmp], check=True)
            (root / '.gitignore').write_text('.local/\nsite-agent.json\n')
            (root / 'README.md').write_text('Public source')
            (root / '.local').mkdir()
            (root / '.local/credential.txt').write_text('private')
            (root / 'site-agent.json').write_text('{}')
            with candidates(root) as (repo, command, names):
                self.assertNotIn('site-agent.json', names)
                self.assertNotIn('.local/credential.txt', names)
                self.assertFalse(check(repo, command, names))
            subprocess.run(['git', '-C', tmp, 'add', '-f', 'site-agent.json'], check=True)
            with candidates(root, True) as (repo, command, names):
                self.assertTrue(check(repo, command, names, True))

    def test_staged_secret_is_detected_even_if_worktree_is_clean(self):
        with tempfile.TemporaryDirectory(prefix='site-public-stage-') as tmp:
            root = Path(tmp)
            subprocess.run(['git', 'init', '--quiet', tmp], check=True)
            p = root / 'notes.md'
            p.write_bytes(b'SITEPAIR1.' + base64.urlsafe_b64encode(bytes(range(64))))
            subprocess.run(['git', '-C', tmp, 'add', 'notes.md'], check=True)
            p.write_text('Secret removed in working tree only')
            with candidates(root, True) as (repo, command, names):
                self.assertTrue(check(repo, command, names, True))
            with candidates(root) as (repo, command, names):
                self.assertFalse(check(repo, command, names))

    def test_server_selection_comes_from_private_config(self):
        with tempfile.TemporaryDirectory(prefix='site-server-settings-') as tmp:
            p = Path(tmp) / 'server.json'
            p.write_text(json.dumps({'host':'hub.example.invalid', 'deviceId':'dev-example'}))
            args = argparse.Namespace(server_config=p, host=None)
            with patch.dict(os.environ, {}, clear=True):
                self.assertEqual(load(args)['host'], 'hub.example.invalid')
                args.host = 'other.example.invalid'
                self.assertEqual(load(args)['host'], args.host)
                args.host = '-oProxyCommand=invalid'
                with self.assertRaises(ValueError):
                    load(args)

    def test_install_templates_preserve_existing_root_and_service_ids(self):
        source_root = Path(__file__).resolve().parents[1]
        tree = ast.parse((source_root / 'deploy/server/install.py').read_text())
        function = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == 'install')
        for prefix in ['sitedeck', 'endless']:
            with tempfile.TemporaryDirectory(prefix='site-render-test-') as temporary:
                root = Path(temporary)
                namespace = dict(os=os, Path=Path, tempfile=tempfile, shutil=shutil,
                                 ROOT=root, SERVICE_PREFIX=prefix, host='hub.example.invalid')
                exec(compile(ast.Module(body=[function], type_ignores=[]), '<template-test>', 'exec'), namespace)
                for name in ['nginx.conf', 'sitedeck-hub.service', 'sitedeck-gateway.service', 'sitedeck-cert-renew.service']:
                    output = root / name
                    namespace['install'](source_root / 'deploy/server' / name, output)
                    text = output.read_text()
                    self.assertNotIn('/root/sitedeck', text)
                    self.assertNotIn('__HUB_HOST__', text)
                    self.assertIn(str(root), text)
                    if name == 'sitedeck-hub.service':
                        self.assertIn('User=' + prefix + '-hub', text)
                    if name == 'nginx.conf':
                        self.assertIn('server_name hub.example.invalid;', text)


if __name__ == '__main__':
    unittest.main()
