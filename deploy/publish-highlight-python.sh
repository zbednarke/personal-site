#!/usr/bin/env bash
# Adapter for the existing byte-verified uploader. Reject archive links/escapes.
set -euo pipefail
[[ $# -eq 2 && $2 =~ ^[0-9a-f]{40}$ ]] || exit 64
python3 - "$1" "$2" <<'PY'
from pathlib import Path, PurePosixPath
import subprocess, sys, tarfile, tempfile
roots = {'upgrade-highlight-python.py', 'verify-jazz-auth.py', 'highlight-runtime-wheels.txt'}
with tempfile.TemporaryDirectory(prefix='jazz-python-security-') as work:
    with tarfile.open(sys.argv[1], 'r:gz') as archive:
        members = archive.getmembers()
        if len(members) > 256 or sum(m.size for m in members) > 64 * 1024 * 1024:
            raise SystemExit('Oversized security bundle')
        seen = set()
        for member in members:
            path = PurePosixPath(member.name)
            allowed = member.name in roots or member.name in ('tools', 'tools/highlight', 'wheels') or member.name.startswith(('tools/highlight/', 'wheels/'))
            if not allowed or path.is_absolute() or '..' in path.parts or member.name in seen or member.size < 0 or not (member.isfile() or member.isdir()):
                raise SystemExit('Invalid security bundle; nothing installed')
            seen.add(member.name)
            target = Path(work) / member.name
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(archive.extractfile(member).read())
        if not roots.issubset(seen):
            raise SystemExit('Incomplete security bundle')
    subprocess.run([sys.executable, str(Path(work) / 'upgrade-highlight-python.py'), work, sys.argv[2]], check=True)
PY
