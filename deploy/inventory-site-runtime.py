"""Read-only inventory of the site's known VM runtimes; never reads app secrets.

Run on actual-server with the existing deploy identity: python3 - < this file.
Does not install packages, restart services, enumerate environment variables,
read application configuration, or print binary contents.
"""
import json
import hashlib
from pathlib import Path
import platform
import re
import subprocess


def output(args):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=15)
    except (OSError, subprocess.TimeoutExpired):
        return None
    return result.stdout.strip() if result.returncode == 0 else None


def go_build_info(data):
    """Read only version/revision from modern Go's inline build-info header."""
    start = data.find(b'\xff Go buildinf:')
    if start < 0 or len(data) < start + 32 or not data[start + 15] & 2:
        return {'status': 'unavailable'}

    def string_at(pos):
        length = 0
        for shift in range(0, 64, 7):
            if pos >= len(data):
                raise ValueError('truncated build info')
            value = data[pos]
            pos += 1
            length |= (value & 127) << shift
            if value < 128:
                if length > 1024 * 1024 or pos + length > len(data):
                    raise ValueError('invalid build info length')
                return data[pos:pos + length].decode('utf-8', errors='replace'), pos + length
        raise ValueError('invalid build info length')

    try:
        version, pos = string_at(start + 32)
        modules, _ = string_at(pos)
    except ValueError:
        return {'status': 'unavailable'}
    if not re.fullmatch(r'go[0-9A-Za-z.+ _:-]{1,100}', version):
        return {'status': 'unavailable'}
    revision = re.search(r'\bvcs\.revision=([0-9a-f]{40})\b', modules)
    return {'status': 'read', 'go_version': version,
            'source_commit': revision.group(1) if revision else None,
            'source_modified': 'vcs.modified=true' in modules}


def auth_build_info(path=Path('/usr/local/bin/jazz-auth')):
    try:
        if path.stat().st_size > 64 * 1024 * 1024:
            return {'status': 'unavailable'}
        return go_build_info(path.read_bytes())
    except OSError:
        return {'status': 'unavailable'}


def inventory():
    release = {}
    try:
        for line in Path('/etc/os-release').read_text().splitlines():
            key, _, value = line.partition('=')
            if key in ('ID', 'VERSION_ID', 'PRETTY_NAME'):
                release[key] = value.strip('"')
    except OSError:
        pass
    packages = {}
    for name in ('caddy', 'ffmpeg', 'python3', 'python3-pil', 'python3-numpy',
                 'libc6', 'libssl3', 'libssl3t64', 'linux-image-cloud-amd64', 'linux-image-amd64', 'linux-image-generic'):
        value = output(['dpkg-query', '-W', '-f=${Version}', name])
        if value is not None:
            packages[name] = value
    python_modules = {}
    proxy_integrity = None
    try:
        expected = Path('/var/lib/site-security-maintenance/caddy-installed.sha256').read_text().strip()
        proxy_integrity = {'managed_record_present': True, 'matches_managed_build': bool(re.fullmatch(r'[0-9a-f]{64}', expected)) and hashlib.sha256(Path('/usr/bin/caddy').read_bytes()).hexdigest() == expected}
    except OSError:
        pass
    for name in ('PIL', 'numpy'):
        python_modules[name] = output(['/usr/bin/python3', '-I', '-c',
                                       'import ' + name + '; print(' + name + '.__version__)'])
    start = output(['systemctl', 'show', 'jazz-highlights', '--property=ExecStart', '--value'])
    runtime = None
    match = re.search(r'\bpath=([^ ;}]+)', start or '')
    if match and (match[1] == '/usr/bin/python3' or re.fullmatch(r'/opt/jazz-highlights-venvs/security-[0-9a-f]{40}-[A-Za-z0-9_]+/bin/python', match[1])):
        runtime = {'interpreter': match[1], 'pillow': None, 'numpy': None}
        for key, module in [('pillow', 'PIL'), ('numpy', 'numpy')]:
            runtime[key] = output([match[1], '-I', '-c', 'import ' + module + '; print(' + module + '.__version__)'])
    return {
        'os': release, 'kernel': platform.release(), 'packages': packages,
        'boot_id': Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
        'system_python_modules': python_modules,
        'highlights_runtime': runtime,
        'highlights_uses_system_python': bool(start and re.search(r'\bpath=/usr/bin/python3(?:[ ;}])', start)),
        'jazz_auth': auth_build_info(),
        'caddy_build': auth_build_info(Path('/usr/bin/caddy')),
        'caddy_version': output(['/usr/bin/caddy', 'version']),
        'caddy_integrity': proxy_integrity,
        'services_active': {name: output(['systemctl', 'is-active', name]) == 'active'
                            for name in ('caddy', 'jazz-auth', 'jazz-highlights')},
        'reboot_required': Path('/var/run/reboot-required').exists(),
    }


if __name__ == '__main__':
    # A fresh gcloud SSH session can print key-generation banners to stdout.
    print('SITE_RUNTIME_INVENTORY=' + json.dumps(inventory(), sort_keys=True))
