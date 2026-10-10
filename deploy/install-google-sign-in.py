"""Switch the private paths to Sign in with Google. Run as root on the site VM.

Prerequisite: install-jazz-auth.py has already put jazz-auth in front of the
private paths. Place this script, verify-jazz-auth.py and the new Linux
jazz-auth binary in one directory, then run, for example:

  gcloud secrets versions access latest --secret=site-google-oauth-secret --project=parabolio-prod \
    | sudo python3 install-google-sign-in.py \
        --client-id 1234-abc.apps.googleusercontent.com \
        --allowed-email you@gmail.com

The client secret is read from stdin or from a root-only file given with
--secret-file, never from the command line. Re-running is safe: an existing
secret is kept when none is supplied, and finished steps are skipped.

It keeps User, Hash and Key in /etc/jazz-auth.json (so the API identity and
signing key are unchanged), adds /auth/* to the live Caddyfile and reserves
/commonplace in the private matcher, validates everything first, then installs,
restarts, verifies and rolls back automatically if anything fails.
"""
from pathlib import Path
import argparse, getpass, json, os, re, shutil, stat, subprocess, sys, tempfile, time, urllib.error, urllib.request

HERE = Path(__file__).resolve().parent
CADDYFILE = Path('/etc/caddy/Caddyfile')
CONFIG = Path('/etc/jazz-auth.json')
BINARY = Path('/usr/local/bin/jazz-auth')
UNIT = Path('/etc/systemd/system/jazz-auth.service')
CADDY_BACKUP = Path('/etc/caddy/Caddyfile.before-google-sign-in')
CONFIG_BACKUP = Path('/etc/jazz-auth.json.before-google-sign-in')
BINARY_BACKUP = Path('/usr/local/bin/jazz-auth.before-google-sign-in')

AUTH_ROUTE = """    route /auth/* {
        reverse_proxy 127.0.0.1:8768
    }
"""
CHECK_ROUTE = re.compile(r'(?m)^([ \t]*)route @jazz_private \{\s*\n\s*reverse_proxy 127\.0\.0\.1:8768 \{')
MATCHER = re.compile(r'(?ms)^([ \t]*@jazz_private \{\s*\n[ \t]*path )([^\n]*)$')
RESERVED = ['/commonplace', '/commonplace/*', '/assets/commonplace/*']


def fail(message):
    raise SystemExit(message)


def read_secret(args, existing):
    if args.secret_file:
        path = Path(args.secret_file)
        info = path.stat()
        if info.st_uid != 0 or info.st_mode & (stat.S_IRWXG | stat.S_IRWXO):
            fail(f'{path} must be owned by root and readable only by root (chmod 600); nothing changed')
        secret = path.read_text().strip()
    elif not sys.stdin.isatty():
        secret = sys.stdin.read().strip()
    elif existing:
        secret = ''
    else:
        secret = getpass.getpass('Google client secret: ').strip()
    secret = secret or existing or ''
    if not secret or any(ch.isspace() for ch in secret):
        fail('No usable Google client secret supplied; nothing changed')
    return secret


def merged_config(args, current):
    for key in ('User', 'Key'):
        if not current.get(key):
            fail(f'{CONFIG} has no {key}; run install-jazz-auth.py first. Nothing changed')
    emails = args.allowed_email or current.get('AllowedEmails') or []
    client_id = args.client_id or current.get('GoogleClientID')
    if not client_id or not emails:
        fail('--client-id and at least one --allowed-email are required; nothing changed')
    existing_secret = current.get('GoogleClientSecret') if current.get('GoogleClientID') == client_id else None
    updated = dict(current)  # User, Hash and Key are preserved untouched.
    updated.update(
        GoogleClientID=client_id,
        GoogleClientSecret=read_secret(args, existing_secret),
        AllowedEmails=sorted({e.strip().lower() for e in emails}),
        PublicOrigin=current.get('PublicOrigin') or 'https://zachbednarke.com',
        SessionDays=args.session_days or current.get('SessionDays') or 30,
        AllowPassword=bool(args.allow_password),
    )
    if args.login_hint:
        updated['LoginHint'] = args.login_hint
    for key in ('User', 'Hash', 'Key'):
        assert updated.get(key) == current.get(key)
    return updated


def updated_caddyfile(text):
    """Add the public /auth/* route and reserve /commonplace. Idempotent."""
    match = CHECK_ROUTE.search(text)
    if not match:
        fail('The jazz-auth check route was not found in the Caddyfile; run install-jazz-auth.py first. Nothing changed')
    if 'route /auth/* {' not in text:
        indent = match.group(1)
        block = ''.join(indent + line[4:] + '\n' if line.strip() else '\n' for line in AUTH_ROUTE.splitlines())
        text = text[:match.start()] + block + text[match.start():]
    matcher = MATCHER.search(text)
    if matcher:
        paths = matcher.group(2).split()
        missing = [p for p in RESERVED if p not in paths]
        if missing:
            text = text[:matcher.start(2)] + ' '.join(paths + missing) + text[matcher.end(2):]
    else:
        print('Note: @jazz_private path list not found; /commonplace was not reserved')
    return text


def write_atomic(path, data, mode, group=None):
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix='.' + path.name + '.')
    with os.fdopen(fd, 'wb') as handle:
        handle.write(data)
    os.chmod(tmp, mode)
    if group:
        shutil.chown(tmp, user='root', group=group)
    os.replace(tmp, path)


def caddy_env():
    env = os.environ.copy()
    for line in Path('/etc/caddy/jazz.env').read_text().splitlines():
        if line.strip() and not line.lstrip().startswith('#'):
            key, value = line.split('=', 1)
            env[key.strip()] = value.strip().strip('"').strip("'")
    return env


def wait_for_service():
    for _ in range(50):
        try:
            with urllib.request.urlopen('http://127.0.0.1:8768/auth/login', timeout=1) as response:
                if response.status == 200:
                    return True
        except (urllib.error.URLError, OSError):
            time.sleep(0.2)
    return False


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--client-id', help='Google OAuth web client ID (not secret)')
    parser.add_argument('--allowed-email', action='append', help='Google account allowed to sign in; repeatable')
    parser.add_argument('--secret-file', help='root-only file holding the client secret (default: stdin)')
    parser.add_argument('--session-days', type=int, help='session lifetime in days (default 30)')
    parser.add_argument('--login-hint', help='account Google should pre-select (default: the only allowed email)')
    parser.add_argument('--allow-password', action='store_true', help='also offer the password form (off by default)')
    parser.add_argument('--binary', default=str(HERE / 'jazz-auth'), help='new jazz-auth binary (default: next to this script)')
    args = parser.parse_args()
    if os.geteuid() != 0:
        fail('Run as root')
    new_binary = Path(args.binary)
    verification = HERE / 'verify-jazz-auth.py'
    if not new_binary.is_file() or not verification.is_file():
        fail(f'Expected {new_binary} and {verification}; nothing changed')

    original_config = CONFIG.read_bytes()
    original_caddy = CADDYFILE.read_text()
    caddy_mode = stat.S_IMODE(CADDYFILE.stat().st_mode)
    original_binary = BINARY.read_bytes() if BINARY.exists() else None
    config = merged_config(args, json.loads(original_config))
    caddy_text = updated_caddyfile(original_caddy)

    # Stage and validate everything before touching the live service.
    staged_binary = BINARY.with_name('jazz-auth.new')
    shutil.copyfile(new_binary, staged_binary)
    staged_binary.chmod(0o755)
    staged_config = CONFIG.with_name('jazz-auth.json.new')
    write_atomic(staged_config, json.dumps(config, indent=2).encode() + b'\n', 0o640, 'caddy')
    env = dict(os.environ, JAZZ_AUTH_CONFIG=str(staged_config))
    result = subprocess.run([str(staged_binary), '-check-config'], env=env, capture_output=True, text=True)
    if result.returncode:
        staged_config.unlink()
        staged_binary.unlink()
        fail('New configuration rejected by jazz-auth: ' + (result.stderr.strip().splitlines() or ['unknown error'])[-1] + '; nothing changed')
    candidate = Path('/etc/caddy/Caddyfile.google-sign-in')
    candidate.write_text(caddy_text)
    result = subprocess.run(['caddy', 'validate', '--config', str(candidate), '--adapter', 'caddyfile'], env=caddy_env(), capture_output=True)
    if result.returncode:
        staged_config.unlink()
        staged_binary.unlink()
        fail('Caddy validation failed; nothing changed')

    # Keep first-run backups for manual rollback.
    for backup, data, mode in ((CADDY_BACKUP, original_caddy.encode(), 0o600), (CONFIG_BACKUP, original_config, 0o600), (BINARY_BACKUP, original_binary, 0o755)):
        if data is not None and not backup.exists():
            write_atomic(backup, data, mode)

    def rollback(reason):
        print(f'{reason}; rolling back')
        write_atomic(CONFIG, original_config, 0o640, 'caddy')
        if original_binary is not None:
            write_atomic(BINARY, original_binary, 0o755)
        subprocess.run(['systemctl', 'restart', 'jazz-auth'])
        write_atomic(CADDYFILE, original_caddy.encode(), caddy_mode)
        subprocess.run(['systemctl', 'reload', 'caddy'])
        fail(f'{reason}; previous sign-in restored')

    os.replace(staged_config, CONFIG)
    os.replace(staged_binary, BINARY)
    subprocess.run(['systemctl', 'daemon-reload'], check=True)
    if subprocess.run(['systemctl', 'restart', 'jazz-auth']).returncode or not wait_for_service():
        rollback('jazz-auth did not start')
    candidate.chmod(caddy_mode)
    os.replace(candidate, CADDYFILE)
    if subprocess.run(['systemctl', 'reload', 'caddy'], capture_output=True).returncode:
        rollback('Caddy reload failed')
    result = subprocess.run(['python3', str(verification)], capture_output=True, text=True)
    print(result.stdout, end='')
    if result.returncode:
        print(result.stderr.strip().splitlines()[-1] if result.stderr.strip() else '')
        rollback('Access verification failed')
    print('Sign in with Google installed and verified. User, password hash and signing key preserved; '
          'existing sessions from the old cookie format must sign in once.')


if __name__ == '__main__':
    main()
