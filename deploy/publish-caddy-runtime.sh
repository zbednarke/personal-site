#!/usr/bin/env bash
set -euo pipefail
[[ $# -eq 2 && $2 =~ ^[0-9a-f]{40}$ ]] || exit 64
python3 - "$1" "$2" <<'PY'
from pathlib import Path
import shutil, subprocess, sys, tarfile, tempfile
allowed = {'caddy', 'upgrade-caddy-runtime.py', 'upgrade-jazz-auth.py', 'upgrade-highlight-python.py', 'inventory-site-runtime.py', 'verify-jazz-auth.py', 'reboot-site-once.py'}
with tempfile.TemporaryDirectory(prefix='caddy-security-') as work:
    with tarfile.open(sys.argv[1], 'r:gz') as archive:
        members = archive.getmembers()
        if len(members) != len(allowed) or {m.name for m in members} != allowed or any(not m.isfile() or m.size < 0 for m in members) or sum(m.size for m in members) > 128 * 1024 * 1024:
            raise SystemExit('Invalid proxy bundle; nothing installed')
        for member in members:
            (Path(work) / member.name).write_bytes(archive.extractfile(member).read())
    subprocess.run([sys.executable, str(Path(work) / 'upgrade-caddy-runtime.py'), work, sys.argv[2]], check=True)
    # Preserve only verified, non-secret diagnostic/one-time-reboot programs
    # for the post-publish step; no credential values are copied.
    target = Path('/usr/local/libexec/site-security')
    if target.is_symlink(): raise SystemExit('Unexpected security helper symlink')
    target.mkdir(mode=0o755, parents=True, exist_ok=True)
    for name in ('inventory-site-runtime.py', 'verify-jazz-auth.py', 'reboot-site-once.py', 'upgrade-highlight-python.py'):
        shutil.copyfile(Path(work) / name, target / name)
        (target / name).chmod(0o755)
PY
