#!/usr/bin/env python3
"""Check release archive metadata, static runtime and accidental local data."""
import os
from pathlib import Path, PurePosixPath
import re
import struct
import sys
import tarfile

root = Path(__file__).resolve().parent.parent
archive = Path(sys.argv[1])
private_markers = [str(Path.home()).encode(), str(root).encode()]
secrets = re.compile(rb'(?:privkey:|psk:)[0-9a-fA-F]{64}\b|handoff1\.[A-Za-z0-9_-]{100,}|(?:ghp_|github_pat_)[A-Za-z0-9_]{30,}')
forbidden = {'state.json', 'jobs.json', 'progress.wal', 'service.lock', '.git', '.env', 'tests', '__pycache__'}
count = 0
binary = None
with tarfile.open(archive) as package:
    seen = set()
    for item in package:
        name = PurePosixPath(item.name)
        assert not name.is_absolute() and '..' not in name.parts and name.parts[0] == 'handoff-cli', 'unsafe archive path'
        assert item.name not in seen, 'duplicate archive entry'
        seen.add(item.name)
        assert item.isdir() or item.isfile(), 'links and special files must not be packaged'
        assert not forbidden.intersection(name.parts), 'private runtime/development file packaged'
        assert not name.name.endswith(('.jsonl', '.log', '.pyc')), 'runtime logs/cache packaged'
        assert item.uid == item.gid == 0 and item.uname == item.gname == '', 'build account metadata in archive'
        if item.isfile():
            data = package.extractfile(item).read()
            for marker in private_markers:
                assert marker not in data and marker.decode().encode('utf-16-le') not in data, 'local profile/build path embedded'
            assert not secrets.search(data), 'possible credential or invitation in artifact'
            if item.name == 'handoff-cli/handoff-cli':
                binary = data
            count += 1
assert binary and binary[:4] == b'\x7fELF' and binary[4:6] == b'\x02\x01', 'expected a Linux ELF64 little-endian binary'
# No interpreter or dynamic linker segments: no host glibc/Python/Qt runtime.
phoff = struct.unpack_from('<Q', binary, 32)[0]
phsize, phnum = struct.unpack_from('<HH', binary, 54)
for i in range(phnum):
    kind = struct.unpack_from('<I', binary, phoff + i * phsize)[0]
    assert kind not in (2, 3), 'runtime is dynamically linked'
print(f'PASS: {count} files; safe archive paths/metadata; static ELF; no local profile paths or credential patterns')
