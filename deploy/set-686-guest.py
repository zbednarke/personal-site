"""Set or disable the /686 guest password on the site VM. Read bcrypt hash on stdin."""
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


CONFIG = Path('/etc/jazz-auth.json')
BINARY = '/usr/local/bin/jazz-auth'


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def verify(config):
    origin = config.get('PublicOrigin') or 'https://zachbednarke.com'
    opener = urllib.request.build_opener(NoRedirect())

    def request(path, headers=None, data=None):
        req = urllib.request.Request(origin + path, data=data, headers=headers or {})
        try:
            with opener.open(req, timeout=20) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()

    guest_hash = config.get('EmberGuestHash', '')
    for path, expected in (('/auth/login?next=%2F686%2F', bool(guest_hash)),
                           ('/auth/login?next=%2Fjazz%2F', False)):
        status, html = request(path)
        if status != 200 or (b'Guest login' in html) != expected:
            raise RuntimeError('Guest form is missing or visible outside /686')
    if not guest_hash:
        return
    def b64(value):
        return base64.urlsafe_b64encode(value).decode().rstrip('=')

    value = f"v2.{b64(config['User'].encode())}.e.-.{int(time.time()) + 300}"
    signature = b64(hmac.new(config['Key'].encode(), f'jazz-session-v2|e|{guest_hash}|{value}'.encode(), hashlib.sha256).digest())
    headers = {'Cookie': f'__Host-ember-guest={value}.{signature}', 'Origin': origin,
               'Content-Type': 'application/json'}
    for path, expected, data in (
        ('/686/', (200,), None),
        ('/assets/686/app.js', (200,), None),
        ('/686/api/v1/686/respond', (422,), b'{}'),
        ('/jazz/', (401,), None),
        ('/jazz/api/v1/state', (401,), None),
        ('/assets/jazz/signin-guard.js', (401,), None),
        ('/commonplace/', (401,), None),
    ):
        status, _ = request(path, headers, data)
        if status not in expected:
            raise RuntimeError(f'Unexpected guest status for {path}: {status}')


def set_guest(guest_hash):
    if os.geteuid() != 0 or CONFIG.is_symlink():
        raise RuntimeError('Run as root with a regular auth configuration file')
    if guest_hash and not re.fullmatch(r'\$2[aby]\$\d{2}\$[./A-Za-z0-9]{53}', guest_hash):
        raise RuntimeError('Expected a bcrypt hash on stdin')
    original = CONFIG.read_bytes()
    config = json.loads(original)
    if guest_hash:
        config['EmberGuestHash'] = guest_hash
    else:
        config.pop('EmberGuestHash', None)
    metadata = CONFIG.stat()

    def stage(data):
        fd, name = tempfile.mkstemp(prefix='.jazz-auth-guest-', dir=CONFIG.parent)
        with os.fdopen(fd, 'wb') as f:
            f.write(data)
            os.fchown(f.fileno(), metadata.st_uid, metadata.st_gid)
            os.fchmod(f.fileno(), metadata.st_mode & 0o777)
        return Path(name)

    candidate = stage((json.dumps(config, indent=2) + '\n').encode())
    try:
        subprocess.run([BINARY, '-check-config'], check=True,
                       env=dict(os.environ, JAZZ_AUTH_CONFIG=str(candidate)))
        if CONFIG.read_bytes() != original:
            raise RuntimeError('Auth configuration changed concurrently')
        candidate.replace(CONFIG)
        try:
            subprocess.run(['systemctl', 'restart', 'jazz-auth'], check=True)
            verify(config)
        except Exception:
            stage(original).replace(CONFIG)
            subprocess.run(['systemctl', 'restart', 'jazz-auth'], check=True)
            raise
    finally:
        candidate.unlink(missing_ok=True)


if __name__ == '__main__':
    if sys.argv[1:] == ['--disable']:
        set_guest('')
    elif not sys.argv[1:]:
        guest_hash = sys.stdin.readline(128).strip()
        if not guest_hash:
            sys.exit('Supply a bcrypt hash on stdin or use --disable')
        set_guest(guest_hash)
    else:
        sys.exit('Usage: set-686-guest.py [--disable]')
    print('Verified /686 guest access policy.')
