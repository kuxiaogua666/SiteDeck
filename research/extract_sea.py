"""Read a Node SEA script from ELF note sections without executing the target."""
import argparse
import hashlib
import json
from pathlib import Path
import struct

parser = argparse.ArgumentParser()
parser.add_argument("binary", type=Path)
parser.add_argument("output", type=Path)
args = parser.parse_args()
data = args.binary.read_bytes()
if data[:6] != b"\x7fELF\x02\x01":
    raise SystemExit("Only little-endian ELF64 is supported")
shoff = struct.unpack_from("<Q", data, 40)[0]
entsize, count = struct.unpack_from("<HH", data, 58)
found = None
for i in range(count):
    section = struct.unpack_from("<IIQQQQIIQQ", data, shoff + i * entsize)
    if section[1] != 7:
        continue
    cursor, end = section[4], section[4] + section[5]
    # Postject may leave overlapping note-section ranges. Inspect aligned note
    # headers rather than assuming the section begins at a valid note boundary.
    while cursor + 12 <= end:
        namesz, descsz, kind = struct.unpack_from("<III", data, cursor)
        name_at = cursor + 12
        desc_at = (name_at + namesz + 3) & ~3
        next_at = (desc_at + descsz + 3) & ~3
        if namesz == 14 and next_at <= len(data) and data[name_at:name_at + namesz].rstrip(b"\0") == b"NODE_SEA_BLOB":
            found = (desc_at, descsz)
            break
        cursor += 4
    if found:
        break
if not found:
    raise SystemExit("No NODE_SEA_BLOB note found")
offset, size = found
blob = memoryview(data)[offset:offset + size]
magic, flags = struct.unpack_from("<II", blob)
if magic != 0x143DA20 or flags & 0x6:
    raise SystemExit(f"Unsupported SEA magic/flags: {magic:#x}/{flags:#x}")
cursor = 9  # magic, flags, code-cache boolean
def read_string():
    global cursor
    length = struct.unpack_from("<Q", blob, cursor)[0]
    cursor += 8
    start = cursor
    if cursor + length > len(blob):
        raise ValueError("SEA string exceeds note bounds")
    value = bytes(blob[cursor:cursor + length])
    cursor += length
    return value, start
name, _ = read_string()
script, script_at = read_string()
script.decode("utf-8")
args.output.parent.mkdir(parents=True, exist_ok=True)
with args.output.open("xb") as stream:
    stream.write(script)
metadata = dict(source=str(args.binary.resolve()), sha256=hashlib.sha256(data).hexdigest(),
                embedded_name=name.decode(), flags=flags, offset=offset + script_at,
                length=len(script), script_sha256=hashlib.sha256(script).hexdigest())
args.output.with_suffix(".provenance.json").write_text(json.dumps(metadata, indent=2) + "\n")
print(json.dumps(metadata, indent=2))
