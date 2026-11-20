"""Protect /686 and route /686/api/* to the private API. Run as root on the site VM.

Install this before the first static release so the page is never public. The normal
post-deploy verification then checks signed-in access to the finished experience.
"""
from pathlib import Path
import argparse, difflib, os, re, stat, subprocess, tempfile

HERE = Path(__file__).resolve().parent
CADDYFILE = Path('/etc/caddy/Caddyfile')
CADDY_ENV = Path('/etc/caddy/jazz.env')
BACKUP = Path('/etc/caddy/Caddyfile.before-686')
CANDIDATE = Path('/etc/caddy/Caddyfile.686')

ROUTE = """# 686 Culpable Ember keeps provider credentials in the private API.
handle_path /686/api/* {
    reverse_proxy {$JAZZ_API_URL} {
        flush_interval -1
        header_up Host {upstream_hostport}
        header_up X-Jazz-User {http.request.header.X-Jazz-User}
        header_up X-Jazz-Gateway-Key {$JAZZ_GATEWAY_KEY}
    }
}

"""
ANCHORS = [
    re.compile(r'(?m)^([ \t]*)handle_path /commonplace/api/\* \{'),
    re.compile(r'(?m)^([ \t]*)handle_path /trumpets/api/\* \{'),
    re.compile(r'(?m)^([ \t]*)handle_path /jazz/api/\* \{'),
]
MATCHER = re.compile(r'(?ms)^([ \t]*@jazz_private \{\s*\n[ \t]*path )([^\n]*)$')
NOT_PRIVATE = re.compile(r'(?ms)^([ \t]*@not_jazz \{\s*\n[ \t]*not path )([^\n]*)$')
RESERVED = ['/686', '/686/*', '/assets/686/*']
INSTALLED = re.compile(r'(?m)^[ \t]*handle_path /686/api/\* \{')


class InstallError(Exception):
    pass


def add_paths(text, matcher, paths):
    match = matcher.search(text)
    if not match:
        return text, False
    present = match.group(2).split()
    missing = [path for path in paths if path not in present]
    if not missing:
        return text, True
    return text[:match.start(2)] + ' '.join(present + missing) + text[match.end(2):], True


def updated_caddyfile(text):
    text, found = add_paths(text, MATCHER, RESERVED)
    if not found:
        raise InstallError('The @jazz_private matcher was not found; install Google sign-in first')
    # A site-wide microphone=() header must not match the 686 page.
    text, _ = add_paths(text, NOT_PRIVATE, RESERVED)
    installed = INSTALLED.search(text)
    if installed:
        # Upgrade the existing route as well as fresh installs. Only inspect
        # this reverse_proxy block, never another application's flush setting.
        tail = text[installed.end():]
        proxy = re.match(r'\s*\n([ \t]*)reverse_proxy \{\$JAZZ_API_URL\} \{\n(.*?)(^[ \t]*\})', tail, re.M | re.S)
        if not proxy:
            raise InstallError('The existing 686 proxy is not in the expected block form')
        body = proxy.group(2)
        if re.search(r'(?m)^[ \t]*flush_interval[ \t]+-1[ \t]*$', body):
            return text
        body = re.sub(r'(?m)^[ \t]*flush_interval[^\n]*\n', '', body)
        body = proxy.group(1) + '    flush_interval -1\n' + body
        start = installed.end() + proxy.start(2)
        end = installed.end() + proxy.end(2)
        return text[:start] + body + text[end:]
    for anchor in ANCHORS:
        match = anchor.search(text)
        if not match:
            continue
        indent = match.group(1)
        block = ''.join(indent + line + '\n' if line.strip() else '\n' for line in ROUTE.splitlines())
        start = match.start()
        lines = text[:start].splitlines(keepends=True)
        while lines and lines[-1].strip().startswith('#'):
            start -= len(lines.pop())
        return text[:start] + block + text[start:]
    raise InstallError('No private API route anchor was found')


def write_atomic(path, data, mode, owner):
    fd, temporary = tempfile.mkstemp(dir=path.parent, prefix='.' + path.name + '.')
    with os.fdopen(fd, 'wb') as handle:
        handle.write(data)
    os.chmod(temporary, mode)
    os.chown(temporary, *owner)
    os.replace(temporary, path)


def caddy_env():
    env = os.environ.copy()
    for line in CADDY_ENV.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            env[key.strip()] = value.strip().strip('"').strip("'")
    return env


def verify_existing_apps():
    env = dict(os.environ, JAZZ_VERIFY_SKIP_686='1')
    result = subprocess.run(['python3', str(HERE / 'verify-jazz-auth.py')], env=env, capture_output=True, text=True)
    print(result.stdout, end='')
    if result.returncode:
        print(result.stderr.strip().splitlines()[-1] if result.stderr.strip() else 'verification failed')
    return result.returncode == 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    original = CADDYFILE.read_text()
    candidate = updated_caddyfile(original)
    if args.dry_run:
        print(''.join(difflib.unified_diff(original.splitlines(True), candidate.splitlines(True), str(CADDYFILE), str(CADDYFILE) + ' (new)')) or 'Already installed.')
        return
    if os.geteuid() != 0:
        raise SystemExit('Run as root')
    info = CADDYFILE.stat()
    mode, owner = stat.S_IMODE(info.st_mode), (info.st_uid, info.st_gid)
    if candidate == original:
        print('The /686 route is already installed; verifying existing private apps.')
        raise SystemExit(0 if verify_existing_apps() else 1)
    write_atomic(CANDIDATE, candidate.encode(), mode, owner)
    validation = subprocess.run(['caddy', 'validate', '--config', str(CANDIDATE), '--adapter', 'caddyfile'], env=caddy_env(), capture_output=True, text=True)
    if validation.returncode:
        CANDIDATE.unlink()
        raise SystemExit('Caddy validation failed; nothing changed\n' + validation.stderr[-2000:])
    if not BACKUP.exists():
        write_atomic(BACKUP, original.encode(), 0o600, (0, 0))

    def rollback(reason):
        write_atomic(CADDYFILE, original.encode(), mode, owner)
        subprocess.run(['systemctl', 'reload', 'caddy'])
        raise SystemExit(reason + '; previous Caddyfile restored')

    os.replace(CANDIDATE, CADDYFILE)
    if subprocess.run(['systemctl', 'reload', 'caddy'], capture_output=True).returncode:
        rollback('Caddy reload failed')
    if not verify_existing_apps():
        rollback('Existing private-app verification failed')
    print('/686 protection and API route installed. Deploy the site, then run verify-jazz-auth.py without the skip flag.')


if __name__ == '__main__':
    main()
