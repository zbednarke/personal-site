"""Install patched Pillow in a private venv; never change app code or apt packages.

Run as root with a verified offline bundle and commit. The existing system
NumPy remains visible. Only this service's ExecStart is changed, and only while
idle. Failure restores the previous override and verifies the old service.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
import urllib.request

SERVICE = 'jazz-highlights'
SYSTEM_PYTHON = Path('/usr/bin/python3')
VENV_ROOT = Path('/opt/jazz-highlights-venvs')
OVERRIDE = Path('/etc/systemd/system/jazz-highlights.service.d/90-security-python.conf')
LIVE_CODE = Path('/opt/jazz-highlights/hosted.py')


class UpgradeError(RuntimeError):
    pass


def checked(args, label):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=240)
    except (OSError, subprocess.TimeoutExpired):
        raise UpgradeError(label + ' failed; subprocess output withheld') from None
    if result.returncode:
        raise UpgradeError(label + ' failed; subprocess output withheld')
    return result.stdout.strip()


def wheelhouse(bundle):
    pins = {}
    for line in (bundle / 'highlight-runtime-wheels.txt').read_text().splitlines():
        if not line or line.startswith('#'):
            continue
        match = re.fullmatch(r'(Pillow|pip)==([0-9.]+) --hash=sha256:([0-9a-f]{64})', line)
        if not match or match[1].lower() in pins:
            raise UpgradeError('Invalid wheel requirements')
        pins[match[1].lower()] = (match[2], match[3])
    if {name: pin[0] for name, pin in pins.items()} != {'pillow': '13.0.0', 'pip': '26.2.2'}:
        raise UpgradeError('Expected the reviewed Pillow and pip versions')
    wheels = {}
    for path in (bundle / 'wheels').glob('*.whl'):
        parts = path.name.split('-')
        name = parts[0].lower()
        if path.is_symlink() or not path.is_file() or len(parts) < 3 or name not in pins or name in wheels or parts[1] != pins[name][0]:
            raise UpgradeError('Unexpected wheel artifact')
        if hashlib.sha256(path.read_bytes()).hexdigest() != pins[name][1]:
            raise UpgradeError('Wheel checksum mismatch; nothing installed')
        wheels[name] = path
    if set(wheels) != set(pins):
        raise UpgradeError('Missing reviewed wheel artifacts')
    return wheels


def versions(python, runner):
    code = "import json,sys,PIL,numpy; print(json.dumps({'pillow':PIL.__version__,'numpy':numpy.__version__,'python':list(sys.version_info[:2])}))"
    try:
        return json.loads(runner([str(python), '-I', '-c', code], 'Runtime version check'))
    except (ValueError, TypeError):
        raise UpgradeError('No valid runtime version receipt') from None


def current_interpreter(start, root=VENV_ROOT, live=LIVE_CODE):
    match = re.search(r'\bpath=([^ ;}]+)', start)
    if not match:
        raise UpgradeError('No known service ExecStart')
    python = match[1]
    known = python == str(SYSTEM_PYTHON) or re.fullmatch(re.escape(str(root)) + r'/security-[0-9a-f]{40}-[A-Za-z0-9_]+/bin/python', python)
    if not known or not re.search(r'argv\[\]=' + re.escape(python + ' ' + str(live)) + r'(?: ;|$)', start):
        raise UpgradeError('Custom service command found; refusing to overwrite it')
    return Path(python)


def unit(python, live=LIVE_CODE):
    check = f"{python} -c \"import PIL; assert PIL.__version__ == '13.0.0'\""
    return f'[Service]\nExecStart=\nExecStart={python} {live}\nExecStartPre={check}\n'.encode()


def atomic_write(path, data):
    fd, name = tempfile.mkstemp(prefix='.security-python-', dir=path.parent)
    staged = Path(name)
    try:
        with os.fdopen(fd, 'wb') as handle:
            handle.write(data)
        staged.chmod(0o644)
        os.replace(staged, path)
    finally:
        staged.unlink(missing_ok=True)


def local_request(path):
    # Same owner-only loopback check used by the existing film installer.
    # Do not print configuration, owner identity, response data or exceptions.
    try:
        owner = json.loads(Path('/etc/jazz-auth.json').read_text())['User']
        request = urllib.request.Request('http://127.0.0.1:8769' + path, headers={'X-Jazz-User': owner})
        with urllib.request.urlopen(request, timeout=5) as response:
            if response.status != 200:
                raise ValueError('Unexpected status')
            return json.load(response)
    except (OSError, ValueError, KeyError, TypeError):
        raise UpgradeError('Cannot verify the local film service; response withheld') from None


def idle():
    jobs = local_request('/jobs')
    if not isinstance(jobs, list):
        raise UpgradeError('Invalid job status; refusing to restart')
    return all(isinstance(job, dict) and job.get('phase') in ('complete', 'failed', 'cancelled') for job in jobs)


def healthy():
    for _ in range(20):
        try:
            for path in ('/jobs', '/edits', '/library'):
                local_request(path)
            return True
        except UpgradeError:
            time.sleep(.25)
    return False


def upgrade(bundle, commit, *, root=VENV_ROOT, override=OVERRIDE, live=LIVE_CODE,
            runner=checked, is_idle=idle, health=healthy):
    if not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise UpgradeError('Invalid commit')
    wheels = wheelhouse(bundle)
    runner(['systemctl', 'is-active', '--quiet', SERVICE], 'Existing film service check')
    start = runner(['systemctl', 'show', SERVICE, '--property=ExecStart', '--value'], 'Service command check')
    previous_python = current_interpreter(start, root, live)
    if override.is_symlink() or override.parent.is_symlink():
        raise UpgradeError('Override paths must not be symlinks')
    previous = override.read_bytes() if override.exists() else None
    if override.is_symlink() or previous is not None and previous != unit(previous_python, live):
        raise UpgradeError('Unrecognized override; nothing changed')
    old = versions(previous_python, runner)
    if old.get('python') != [3, 11]:
        raise UpgradeError('Reviewed wheels require the known Python 3.11 runtime')
    if old.get('pillow') == '13.0.0':
        if not health():
            raise UpgradeError('Existing patched runtime is not healthy')
        return previous_python
    if not is_idle():
        raise UpgradeError('Render or analysis is active; no restart performed')
    if root.is_symlink():
        raise UpgradeError('Venv root must not be a symlink')
    root.mkdir(mode=0o755, parents=True, exist_ok=True)
    if root.stat().st_mode & 0o022:
        raise UpgradeError('Venv root is writable by other users')
    target = Path(tempfile.mkdtemp(prefix='security-' + commit + '-', dir=root))
    target.chmod(0o755)
    runner([str(SYSTEM_PYTHON), '-m', 'venv', '--without-pip', '--system-site-packages', str(target)], 'Offline venv creation')
    python = target / 'bin/python'
    bootstrap = "import sys; sys.path.insert(0,sys.argv[1]); from pip._internal.cli.main import main; raise SystemExit(main(['--isolated','install','--no-index','--no-deps','--ignore-installed','--no-compile',sys.argv[2]]))"
    runner([str(python), '-I', '-c', bootstrap, str(wheels['pip']), str(wheels['pillow'])], 'Offline pinned Pillow installation')
    updated = versions(python, runner)
    if updated.get('pillow') != '13.0.0' or updated.get('numpy') != old.get('numpy') or updated.get('python') != old.get('python'):
        raise UpgradeError('Patched Pillow or preserved NumPy/interpreter not verified; service untouched')
    runner([str(python), '-m', 'unittest', 'discover', '-s', str(bundle / 'tools/highlight'), '-p', 'test_*.py'], 'Patched media compatibility tests')
    runner([sys.executable, str(bundle / 'verify-jazz-auth.py')], 'Baseline private-route verification')
    if not is_idle():
        raise UpgradeError('A job started during staging; no restart performed')
    if (override.read_bytes() if override.exists() else None) != previous:
        raise UpgradeError('Override changed concurrently; service untouched')
    override.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    if previous is not None:
        (target / 'previous-override.conf').write_bytes(previous)
    try:
        atomic_write(override, unit(python, live))
        runner(['systemctl', 'daemon-reload'], 'Film service definition reload')
        runner(['systemctl', 'restart', SERVICE], 'Patched film service restart')
        if not health():
            raise UpgradeError('Patched film service health check failed')
        runner([sys.executable, str(bundle / 'verify-jazz-auth.py')], 'Patched private-route verification')
    except Exception:
        try:
            if previous is None:
                override.unlink(missing_ok=True)
            else:
                atomic_write(override, previous)
            runner(['systemctl', 'daemon-reload'], 'Previous service definition reload')
            runner(['systemctl', 'restart', SERVICE], 'Previous film service restart')
            if not health():
                raise UpgradeError('Previous film service health check failed')
        except Exception:
            raise UpgradeError('Film runtime upgrade and rollback failed; operator intervention required') from None
        raise UpgradeError('Film runtime upgrade failed; previous service restored and healthy') from None
    return python


if __name__ == '__main__':
    try:
        if os.geteuid() != 0 or len(sys.argv) != 3:
            raise UpgradeError('Run as root with BUNDLE COMMIT')
        python = upgrade(Path(sys.argv[1]), sys.argv[2])
        print('Pillow 13.0.0 verified; system packages, NumPy and service code unchanged. Interpreter: ' + str(python))
    except (UpgradeError, OSError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
