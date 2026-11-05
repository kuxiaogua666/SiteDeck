"""Create an inspected source-only archive without runtime state or credentials."""
import argparse
import hashlib
import json
from pathlib import Path
import tarfile
import tempfile
import os

from check_public_repo import ROOT, candidates, check

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output', type=Path, default=ROOT / '.local/publication/sitedeck-source.tar.gz')
args = parser.parse_args()
with candidates() as (root, command, names):
    issues = check(root, command, names)
    if issues:
        for name, reasons in issues:
            print(name + ': ' + '; '.join(reasons))
        raise SystemExit('Refusing to export source with private content')
    destination = args.output.resolve()
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, temporary = tempfile.mkstemp(prefix='.source-', dir=destination.parent)
    os.close(fd)
    try:
        with tarfile.open(temporary, 'w:gz', dereference=False) as archive:
            for name in names:
                path = root / name
                if path.is_file():
                    archive.add(path, arcname='sitedeck/' + name, recursive=False)
        os.replace(temporary, destination)
    finally:
        Path(temporary).unlink(missing_ok=True)
print(json.dumps({'archive': str(destination), 'sourceFiles': len(names),
                  'sha256': hashlib.sha256(destination.read_bytes()).hexdigest()}))
