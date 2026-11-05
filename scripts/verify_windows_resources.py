"""Verify shipped PE icons, file version, architecture and GUI subsystem."""
import json
from pathlib import Path
import struct
import re

ROOT = Path(__file__).resolve().parents[1]
version_expected = re.search(r'const Version = "([^"]+)"', (ROOT / 'internal/protocol/protocol.go').read_text()).group(1)
rows = []
for arch, machine in [('amd64', 0x8664), ('arm64', 0xaa64)]:
    for role in ['agent', 'agent-ui', 'cli', 'hub']:
        p = ROOT / 'bin' / ('windows-' + arch) / ('site-' + role + '.exe')
        data = p.read_bytes()
        pe = struct.unpack_from('<I', data, 0x3c)[0]
        assert data[pe:pe+4] == b'PE\0\0'
        actual, sections = struct.unpack_from('<HH', data, pe+4)
        assert actual == machine, p
        opt_size = struct.unpack_from('<H', data, pe+20)[0]
        subsystem = struct.unpack_from('<H', data, pe+24+68)[0]
        assert subsystem == (2 if role == 'agent-ui' else 3), p
        section_table = pe+24+opt_size
        resource = None
        for index in range(sections):
            base = section_table+index*40
            name = data[base:base+8].rstrip(b'\0')
            if name == b'.rsrc':
                rva = struct.unpack_from('<I', data, base+12)[0]
                raw_size, raw = struct.unpack_from('<II', data, base+16)
                resource = data[raw:raw+raw_size]
        assert resource is not None, p

        def directory(offset):
            names, ids = struct.unpack_from('<HH', resource, offset+12)
            return dict(struct.unpack_from('<II', resource, offset+16+i*8) for i in range(names+ids))

        types = directory(0)
        assert {3, 14, 16, 24} <= types.keys(), p

        def leaf(kind, identifier):
            named = directory(types[kind] & 0x7fffffff)
            languages = directory(named[identifier] & 0x7fffffff)
            entry = next(iter(languages.values()))
            start, length = struct.unpack_from('<II', resource, entry)
            return resource[start-rva:start-rva+length]

        group = leaf(14, 1)
        count = struct.unpack_from('<H', group, 4)[0]
        sizes = sorted((group[6+i*14] or 256) for i in range(count))
        assert sizes == [16, 24, 32, 48, 64, 128, 256], (p, sizes)
        version = leaf(16, 1).decode('utf-16le', errors='ignore')
        assert version_expected in version and 'SiteDeck' in version, p
        assert b'requestedExecutionLevel' in leaf(24, 1), p
        rows.append(dict(file=str(p.relative_to(ROOT)), architecture=arch, iconSizes=sizes, subsystem=subsystem, version=version_expected))
result = dict(passed=len(rows), checks=rows)
(ROOT / 'bin/windows-resource-check.json').write_text(json.dumps(result, indent=2)+'\n')
print(json.dumps(result, indent=2))
