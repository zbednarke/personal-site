"""Route /commonplace/api/* to the private API. Run as root on the site VM.

Prerequisite: Sign in with Google is installed (install-google-sign-in.py), so
/commonplace is already in the @jazz_private matcher. Place this script and
verify-jazz-auth.py in one directory, then run:

  sudo python3 install-commonplace-route.py            # install, verify, roll back on failure
  sudo python3 install-commonplace-route.py --dry-run  # show the change, touch nothing

It adds a `handle_path /commonplace/api/*` block next to /trumpets/api/* with
the same gateway headers (the verified X-Jazz-User and the gateway key), and
makes sure /commonplace, /commonplace/* and /assets/commonplace/* are private.
Re-running is safe: an installed route is left alone and only verified.

The Caddyfile keeps its owner, group and mode (Caddy reads it through its
group; see #78). The candidate is checked with `caddy validate` using
/etc/caddy/jazz.env, then Caddy is reloaded and verify-jazz-auth.py is run; any
failure restores the previous Caddyfile and reloads again.
"""
from pathlib import Path
import argparse, difflib, os, re, stat, subprocess, sys, tempfile

HERE = Path(__file__).resolve().parent
CADDYFILE = Path('/etc/caddy/Caddyfile')
CADDY_ENV = Path('/etc/caddy/jazz.env')
BACKUP = Path('/etc/caddy/Caddyfile.before-commonplace')
CANDIDATE = Path('/etc/caddy/Caddyfile.commonplace')

ROUTE = """# Commonplace inherits the same private gateway: Caddy strips the prefix,
# forwards the verified user and adds the gateway secret.
handle_path /commonplace/api/* {
    reverse_proxy {$JAZZ_API_URL} {
        header_up Host {upstream_hostport}
        header_up X-Jazz-User {http.request.header.X-Jazz-User}
        header_up X-Jazz-Gateway-Key {$JAZZ_GATEWAY_KEY}
    }
}

"""
ANCHORS = [
    re.compile(r'(?m)^([ \t]*)handle_path /trumpets/api/\* \{'),
    re.compile(r'(?m)^([ \t]*)handle_path /jazz/api/\* \{'),
]
MATCHER = re.compile(r'(?ms)^([ \t]*@jazz_private \{\s*\n[ \t]*path )([^\n]*)$')
RESERVED = ['/commonplace', '/commonplace/*', '/assets/commonplace/*']
INSTALLED = re.compile(r'(?m)^[ \t]*handle_path /commonplace/api/\* \{')


class InstallError(Exception):
    pass


def updated_caddyfile(text):
    """Return the Caddyfile with the Commonplace route and private paths. Idempotent."""
    matcher = MATCHER.search(text)
    if not matcher:
        raise InstallError('The @jazz_private matcher was not found; install Sign in with Google first. Nothing changed')
    paths = matcher.group(2).split()
    missing = [p for p in RESERVED if p not in paths]
    if missing:
        text = text[:matcher.start(2)] + ' '.join(paths + missing) + text[matcher.end(2):]
    if INSTALLED.search(text):
        return text
    for anchor in ANCHORS:
        match = anchor.search(text)
        if match:
            indent = match.group(1)
            block = ''.join(indent + line + '\n' if line.strip() else '\n' for line in ROUTE.splitlines())
            # Insert before the anchor's own comment lines, keeping them attached to it.
            start = match.start()
            lines_before = text[:start].splitlines(keepends=True)
            while lines_before and lines_before[-1].strip().startswith('#'):
                start -= len(lines_before.pop())
            return text[:start] + block + text[start:]
    raise InstallError('Neither /trumpets/api/* nor /jazz/api/* was found to place the route next to. Nothing changed')


def write_atomic(path, data, mode, owner):
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix='.' + path.name + '.')
    with os.fdopen(fd, 'wb') as handle:
        handle.write(data)
    os.chmod(tmp, mode)
    os.chown(tmp, *owner)
    os.replace(tmp, path)


def caddy_env():
    env = os.environ.copy()
    for line in CADDY_ENV.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            env[key.strip()] = value.strip().strip('"').strip("'")
    return env


def verify():
    script = HERE / 'verify-jazz-auth.py'
    result = subprocess.run(['python3', str(script)], capture_output=True, text=True)
    print(result.stdout, end='')
    if result.returncode:
        print(result.stderr.strip().splitlines()[-1] if result.stderr.strip() else 'verification failed')
    return result.returncode == 0


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--dry-run', action='store_true', help='print the change and exit without touching anything')
    args = parser.parse_args()
    original = CADDYFILE.read_text()
    try:
        candidate = updated_caddyfile(original)
    except InstallError as error:
        raise SystemExit(str(error))
    if args.dry_run:
        diff = ''.join(difflib.unified_diff(original.splitlines(True), candidate.splitlines(True), str(CADDYFILE), str(CADDYFILE) + ' (new)'))
        print(diff or 'Already installed; nothing to change.')
        return
    if os.geteuid() != 0:
        raise SystemExit('Run as root')
    if not (HERE / 'verify-jazz-auth.py').is_file():
        raise SystemExit(f'Expected verify-jazz-auth.py next to this script; nothing changed')
    if candidate == original:
        print('The /commonplace/api route is already installed; verifying.')
        raise SystemExit(0 if verify() else 1)

    info = CADDYFILE.stat()
    mode, owner = stat.S_IMODE(info.st_mode), (info.st_uid, info.st_gid)
    write_atomic(CANDIDATE, candidate.encode(), mode, owner)
    result = subprocess.run(['caddy', 'validate', '--config', str(CANDIDATE), '--adapter', 'caddyfile'], env=caddy_env(), capture_output=True, text=True)
    if result.returncode:
        CANDIDATE.unlink()
        print(result.stderr.strip()[-2000:])
        raise SystemExit('Caddy validation failed; nothing changed')
    if not BACKUP.exists():
        write_atomic(BACKUP, original.encode(), 0o600, (0, 0))

    def rollback(reason):
        print(f'{reason}; rolling back')
        write_atomic(CADDYFILE, original.encode(), mode, owner)
        subprocess.run(['systemctl', 'reload', 'caddy'])
        raise SystemExit(f'{reason}; the previous Caddyfile is restored')

    os.replace(CANDIDATE, CADDYFILE)
    after = CADDYFILE.stat()
    if (after.st_uid, after.st_gid, stat.S_IMODE(after.st_mode)) != (owner[0], owner[1], mode):
        rollback('The Caddyfile owner, group or mode changed')
    if subprocess.run(['systemctl', 'reload', 'caddy'], capture_output=True).returncode:
        rollback('Caddy reload failed')
    if not verify():
        rollback('Access verification failed')
    print('Commonplace API route installed and verified; the Caddyfile kept its owner, group and mode.')


if __name__ == '__main__':
    main()
