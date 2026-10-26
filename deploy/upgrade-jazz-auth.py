"""Replace only jazz-auth's binary, preserving configuration and rolling back.

Invoked as root by publish-auth-runtime.sh after the existing verified uploader.
The bundle's accepted-source list is produced by CI from Git history plus an
explicitly recovered, code-identical pre-import auth commit.
"""
import hashlib
import importlib.util
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import urllib.request

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('runtime_inventory', HERE / 'inventory-site-runtime.py')
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


class UpgradeError(RuntimeError):
    pass


def checked(args, label, **kwargs):
    try:
        result = subprocess.run(args, capture_output=True, timeout=180, **kwargs)
    except (OSError, subprocess.TimeoutExpired):
        raise UpgradeError(label + ' failed; subprocess output withheld') from None
    if result.returncode:
        raise UpgradeError(label + ' failed; subprocess output withheld')


def compiler_is_patched(value):
    match = re.fullmatch(r'go(\d+)\.(\d+)(?:\.(\d+))?', value or '')
    return bool(match and tuple(int(x or 0) for x in match.groups()) >= (1, 27, 3))


def validate_builds(old, new, commit, accepted):
    if old.get('status') != 'read' or old.get('source_modified') or old.get('source_commit') not in accepted:
        raise UpgradeError('The installed auth source is unaccounted for; refusing to overwrite it')
    if new.get('status') != 'read' or new.get('source_modified') or new.get('source_commit') != commit or not compiler_is_patched(new.get('go_version')):
        raise UpgradeError('The candidate is not a clean, patched build of this commit')


def fingerprint(paths):
    return {str(p): (hashlib.sha256(p.read_bytes()).digest(), p.stat().st_uid,
                     p.stat().st_gid, stat.S_IMODE(p.stat().st_mode)) for p in paths}


def ready():
    for _ in range(40):
        try:
            with urllib.request.urlopen('http://127.0.0.1:8768/auth/login', timeout=1) as response:
                if response.status == 200:
                    return True
        except OSError:
            pass
        time.sleep(.25)
    return False


def atomic_binary(path, source, owner, mode):
    fd, name = tempfile.mkstemp(prefix='.jazz-auth-security-', dir=path.parent)
    staged = Path(name)
    try:
        with os.fdopen(fd, 'wb') as handle:
            handle.write(source.read_bytes())
        os.chown(staged, *owner)
        staged.chmod(mode)
        os.replace(staged, path)
    finally:
        staged.unlink(missing_ok=True)


def upgrade(bundle, commit, *, binary=Path('/usr/local/bin/jazz-auth'),
            preserved=(Path('/etc/jazz-auth.json'), Path('/etc/caddy/Caddyfile')),
            backups=Path('/var/backups/jazz-auth-security'), runner=checked, health=ready):
    if not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise UpgradeError('Invalid target commit')
    candidate = bundle / 'jazz-auth'
    if binary.is_symlink() or not binary.is_file() or candidate.is_symlink() or not candidate.is_file():
        raise UpgradeError('Expected regular installed and candidate binaries')
    old_stat = binary.stat()
    if old_stat.st_mode & 0o022:
        raise UpgradeError('Installed auth binary is writable by other users; inspect it first')
    accepted = set((bundle / 'accepted-auth-sources.txt').read_text().splitlines())
    validate_builds(inventory.auth_build_info(binary), inventory.auth_build_info(candidate), commit, accepted)
    before = fingerprint(preserved)
    runner(['systemctl', 'is-active', '--quiet', 'jazz-auth'], 'Existing auth service check')
    candidate.chmod(0o755)
    env = dict(os.environ, JAZZ_AUTH_CONFIG=str(preserved[0]))
    runner([str(candidate), '-check-config'], 'Existing configuration validation', env=env)
    runner([sys.executable, str(bundle / 'verify-jazz-auth.py')], 'Baseline private-route verification')
    if fingerprint(preserved) != before:
        raise UpgradeError('Configuration changed concurrently; nothing installed')
    if candidate.read_bytes() == binary.read_bytes():
        return None
    if backups.is_symlink():
        raise UpgradeError('Backup directory must not be a symlink')
    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    if backups.stat().st_mode & 0o077:
        raise UpgradeError('Backup directory must be private')
    backup = Path(tempfile.mkdtemp(prefix=commit[:12] + '-', dir=backups)) / 'jazz-auth'
    shutil.copy2(binary, backup)
    owner, mode = (old_stat.st_uid, old_stat.st_gid), stat.S_IMODE(old_stat.st_mode)
    try:
        atomic_binary(binary, candidate, owner, mode)
        runner(['systemctl', 'restart', 'jazz-auth'], 'Patched auth restart')
        if not health():
            raise UpgradeError('Patched auth readiness check failed')
        runner([sys.executable, str(bundle / 'verify-jazz-auth.py')], 'Patched private-route verification')
        if fingerprint(preserved) != before:
            raise UpgradeError('Configuration changed concurrently during verification')
    except Exception:
        try:
            atomic_binary(binary, backup, owner, mode)
            runner(['systemctl', 'restart', 'jazz-auth'], 'Previous auth restart')
            if not health():
                raise UpgradeError('Previous auth readiness check failed')
        except Exception:
            raise UpgradeError('Auth upgrade and rollback failed; operator intervention is required') from None
        raise UpgradeError('Auth upgrade failed; previous binary restored and running') from None
    return backup


if __name__ == '__main__':
    try:
        if os.geteuid() != 0 or len(sys.argv) != 3:
            raise UpgradeError('Run as root with BUNDLE COMMIT')
        backup = upgrade(Path(sys.argv[1]), sys.argv[2])
        print('Patched auth verified; configuration, sessions and Caddy file unchanged. Backup: ' + str(backup))
    except (UpgradeError, OSError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
