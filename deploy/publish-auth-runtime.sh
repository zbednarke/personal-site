#!/usr/bin/env bash
# Adapter for upload-static-release.sh's verified, root publish operation.
set -euo pipefail
[[ $# -eq 2 && $2 =~ ^[0-9a-f]{40}$ ]] || exit 64
python3 - "$1" "$2" <<'PY'
from pathlib import Path
import subprocess, sys, tarfile, tempfile
allowed = {'jazz-auth', 'upgrade-jazz-auth.py', 'inventory-site-runtime.py', 'verify-jazz-auth.py', 'accepted-auth-sources.txt'}
with tempfile.TemporaryDirectory(prefix='jazz-auth-security-') as work:
    with tarfile.open(sys.argv[1], 'r:gz') as archive:
        members = archive.getmembers()
        if len(members) != len(allowed) or {m.name for m in members} != allowed or any(not m.isfile() for m in members) or sum(m.size for m in members) > 64 * 1024 * 1024:
            raise SystemExit('Invalid auth security bundle; nothing installed')
        for member in members:
            (Path(work) / member.name).write_bytes(archive.extractfile(member).read())
    subprocess.run([sys.executable, str(Path(work) / 'upgrade-jazz-auth.py'), work, sys.argv[2]], check=True)
PY
