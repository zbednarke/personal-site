"""Install the scanned standard Caddy build without changing service/configuration.

Preserves the installed binary's ownership, mode and bind capabilities. Validates
the current configuration as caddy with its configured environment files, checks
module compatibility/private access, and restores the previous binary on failure.
"""
import errno
import hashlib
import importlib.util
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import uuid

HERE = Path(__file__).resolve().parent


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    return module


auth = load('auth_upgrade', 'upgrade-jazz-auth.py')
films = load('film_upgrade', 'upgrade-highlight-python.py')
UpgradeError = auth.UpgradeError


def capture(args, label, **kwargs):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=180, **kwargs)
    except (OSError, subprocess.TimeoutExpired):
        raise UpgradeError(label + ' failed; output withheld') from None
    if result.returncode:
        raise UpgradeError(label + ' failed; output withheld')
    return result.stdout.strip()


def modules(text):
    result = {line.strip() for line in text.splitlines() if re.fullmatch(r'[a-z][a-z0-9_.-]+', line.strip())}
    if not {'http.handlers.reverse_proxy', 'http.handlers.file_server', 'tls.issuance.acme'} <= result:
        raise UpgradeError('No complete standard module inventory')
    return result


def release(text):
    match = re.match(r'^v(\d+)\.(\d+)\.(\d+)(?:\s|$)', text)
    if not match:
        raise UpgradeError('Unrecognized proxy version; no downgrade attempted')
    return tuple(map(int, match.groups()))


def validation_command(candidate, config, environment_files, home, working_directory=''):
    command = ['systemd-run', '--quiet', '--wait', '--pipe', '--collect',
               '--unit=caddy-security-validate-' + uuid.uuid4().hex,
               '--uid=caddy', '--gid=caddy', '--setenv=HOME=' + home]
    if working_directory:
        if not working_directory.startswith('/'):
            raise UpgradeError('Unexpected service working-directory metadata')
        command.append('--property=WorkingDirectory=' + working_directory)
    files = re.findall(r'(/[^\s]+) \(ignore_errors=(yes|no)\)', environment_files)
    if environment_files.strip() and not files:
        raise UpgradeError('Unrecognized environment-file metadata')
    for path, optional in files:
        command.append('--property=EnvironmentFile=' + ('-' if optional == 'yes' else '') + path)
    return command + [str(candidate), 'validate', '--config', str(config), '--adapter', 'caddyfile']


def capabilities(path):
    try:
        return os.getxattr(path, 'security.capability')
    except OSError as error:
        if error.errno in (errno.ENODATA, errno.ENOTSUP): return None
        raise


def replace_binary(path, source, original, caps):
    fd, name = tempfile.mkstemp(prefix='.caddy-security-', dir=path.parent)
    staged = Path(name)
    try:
        with os.fdopen(fd, 'wb') as handle: handle.write(source.read_bytes())
        os.chown(staged, original.st_uid, original.st_gid)
        staged.chmod(stat.S_IMODE(original.st_mode))
        if caps is not None: os.setxattr(staged, 'security.capability', caps)
        os.replace(staged, path)
    finally:
        staged.unlink(missing_ok=True)


def running_binary(binary, runner):
    pid = runner(['systemctl', 'show', 'caddy', '--property=MainPID', '--value'], 'Proxy process check')
    if not pid.isdigit() or int(pid) == 0: return False
    try:
        process, installed = Path('/proc/' + pid + '/exe').stat(), binary.stat()
        return (process.st_dev, process.st_ino) == (installed.st_dev, installed.st_ino)
    except OSError:
        return False


def upgrade(bundle, commit, *, binary=Path('/usr/bin/caddy'),
            preserved=(Path('/etc/caddy/Caddyfile'), Path('/etc/jazz-auth.json')),
            backups=Path('/var/backups/caddy-security'),
            receipt=Path('/var/lib/site-security-maintenance/caddy-installed.sha256'),
            runner=capture, is_idle=films.idle, active=running_binary, home=None):
    if not re.fullmatch(r'[0-9a-f]{40}', commit): raise UpgradeError('Invalid commit')
    candidate = bundle / 'caddy'
    if binary.is_symlink() or candidate.is_symlink() or not binary.is_file() or not candidate.is_file():
        raise UpgradeError('Expected regular proxy binaries')
    original, caps = binary.stat(), capabilities(binary)
    if original.st_uid != os.geteuid() or original.st_mode & 0o022:
        raise UpgradeError('Proxy binary ownership/permissions need inspection')
    build = auth.inventory.auth_build_info(candidate)
    if build.get('source_commit') != commit or build.get('source_modified') or not auth.compiler_is_patched(build.get('go_version')):
        raise UpgradeError('Candidate is not a clean patched build of this commit')
    candidate.chmod(0o755)
    if release(runner([str(candidate), 'version'], 'Candidate version')) < release(runner([str(binary), 'version'], 'Installed version')):
        raise UpgradeError('Refusing proxy downgrade')
    old_modules = modules(runner([str(binary), 'list-modules'], 'Installed module inventory'))
    new_modules = modules(runner([str(candidate), 'list-modules'], 'Candidate module inventory'))
    if not old_modules <= new_modules: raise UpgradeError('Candidate omits installed modules; refusing replacement')
    before = auth.fingerprint(preserved)
    old_digest = hashlib.sha256(binary.read_bytes()).digest()
    runner(['systemctl', 'is-active', '--quiet', 'caddy'], 'Existing proxy service check')
    runner([sys.executable, str(bundle / 'verify-jazz-auth.py')], 'Baseline private-route verification')
    if not is_idle(): raise UpgradeError('Film job active; no proxy restart performed')
    environment = runner(['systemctl', 'show', 'caddy', '--property=EnvironmentFiles', '--value'], 'Environment-file metadata')
    working_directory = runner(['systemctl', 'show', 'caddy', '--property=WorkingDirectory', '--value'], 'Working-directory metadata')
    fd, name = tempfile.mkstemp(prefix='.caddy-validate-', dir=binary.parent); os.close(fd)
    staged = Path(name)
    try:
        staged.write_bytes(candidate.read_bytes()); staged.chmod(0o755)
        runner(validation_command(staged, preserved[0], environment, home or pwd.getpwnam('caddy').pw_dir, working_directory), 'Current proxy configuration validation')
    finally:
        staged.unlink(missing_ok=True)
    if not is_idle() or auth.fingerprint(preserved) != before or hashlib.sha256(binary.read_bytes()).digest() != old_digest:
        raise UpgradeError('Activity or concurrent changes found; nothing installed')
    if candidate.read_bytes() == binary.read_bytes(): return None
    if backups.is_symlink(): raise UpgradeError('Backup path must not be a symlink')
    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    if backups.stat().st_uid != os.geteuid() or backups.stat().st_mode & 0o077:
        raise UpgradeError('Backup directory must be private and operator-owned')
    backup = Path(tempfile.mkdtemp(prefix=commit[:12] + '-', dir=backups)) / 'caddy'
    shutil.copy2(binary, backup)
    try:
        replace_binary(binary, candidate, original, caps)
        runner(['systemctl', 'restart', 'caddy'], 'Patched proxy restart')
        if not active(binary, runner): raise UpgradeError('New proxy process does not match the installed binary')
        runner([sys.executable, str(bundle / 'verify-jazz-auth.py')], 'Patched private-route verification')
        if auth.fingerprint(preserved) != before: raise UpgradeError('Configuration changed concurrently')
    except Exception:
        try:
            replace_binary(binary, backup, original, caps)
            runner(['systemctl', 'restart', 'caddy'], 'Previous proxy restart')
            if not active(binary, runner): raise UpgradeError('Previous proxy process did not recover')
        except Exception:
            raise UpgradeError('Proxy upgrade and rollback failed; operator intervention required') from None
        raise UpgradeError('Proxy upgrade failed; previous binary restored and running') from None
    receipt.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    films.atomic_write(receipt, (hashlib.sha256(binary.read_bytes()).hexdigest() + '\n').encode())
    return backup


if __name__ == '__main__':
    try:
        if os.geteuid() != 0 or len(sys.argv) != 3: raise UpgradeError('Run as root with BUNDLE COMMIT')
        backup = upgrade(Path(sys.argv[1]), sys.argv[2])
        print('Patched proxy verified; configuration and service definition unchanged. Backup: ' + str(backup))
    except (UpgradeError, OSError) as error:
        print(str(error), file=sys.stderr); raise SystemExit(1)
