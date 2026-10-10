"""Route /workbench/api/* to the Workbench Cloud Run service. Run as root on the site VM.

Prerequisites: Sign in with Google is installed (install-google-sign-in.py),
and /etc/caddy/jazz.env has WORKBENCH_API_URL=<the workbench service URL>
(the same kind of value as JAZZ_API_URL). Place this script and
verify-jazz-auth.py in one directory, then run:

  sudo python3 install-workbench-route.py            # install, verify, roll back on failure
  sudo python3 install-workbench-route.py --dry-run  # show the change, touch nothing

It adds a `handle_path /workbench/api/*` block next to /jazz/api/* with the
same gateway headers (the verified X-Jazz-User and the gateway key), and an
immediate flush so the event stream is not buffered. It makes /workbench and
/workbench/* private (signed in, like /jazz), and lets /trumpets and
/commonplace use the microphone for hold-to-talk, as /jazz already can.
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
BACKUP = Path('/etc/caddy/Caddyfile.before-workbench')
CANDIDATE = Path('/etc/caddy/Caddyfile.workbench')

ROUTE = """# Workbench (the owner-only agent sheet) is a separate Cloud Run service
# from the same image. Same private gateway; streams are flushed immediately.
# deploy/install-workbench-route.py adds this block to a live Caddyfile.
handle_path /workbench/api/* {
    reverse_proxy {$WORKBENCH_API_URL} {
        header_up Host {upstream_hostport}
        header_up X-Jazz-User {http.request.header.X-Jazz-User}
        header_up X-Jazz-Gateway-Key {$JAZZ_GATEWAY_KEY}
        flush_interval -1
    }
}

"""
ANCHORS = [
    re.compile(r'(?m)^([ \t]*)handle_path /jazz/api/\* \{'),
    re.compile(r'(?m)^([ \t]*)handle_path /trumpets/api/\* \{'),
]
PRIVATE = re.compile(r'(?ms)^([ \t]*@jazz_private \{\s*\n[ \t]*path )([^\n]*)$')
PRIVATE_PATHS = ['/workbench', '/workbench/*']
# The site-wide "microphone=()" policy must not apply to pages with hold-to-talk.
NOT_JAZZ = re.compile(r'(?ms)^([ \t]*@not_jazz \{\s*\n[ \t]*not path )([^\n]*)$')
MIC_PATHS = ['/trumpets', '/trumpets/*', '/commonplace', '/commonplace/*']
INSTALLED = re.compile(r'(?m)^[ \t]*handle_path /workbench/api/\* \{')


class InstallError(Exception):
    pass


def add_paths(text, pattern, wanted):
    match = pattern.search(text)
    if not match:
        return text, False
    paths = match.group(2).split()
    missing = [p for p in wanted if p not in paths]
    if missing:
        text = text[:match.start(2)] + ' '.join(paths + missing) + text[match.end(2):]
    return text, True


def updated_caddyfile(text):
    """Return the Caddyfile with the Workbench route and paths. Idempotent."""
    text, found = add_paths(text, PRIVATE, PRIVATE_PATHS)
    if not found:
        raise InstallError('The @jazz_private matcher was not found; install Sign in with Google first. Nothing changed')
    text, _ = add_paths(text, NOT_JAZZ, MIC_PATHS)
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
    raise InstallError('Neither /jazz/api/* nor /trumpets/api/* was found to place the route next to. Nothing changed')


def write_atomic(path, data, mode, owner):
    fd, tmp = tempfile.mkstemp(dir=path.parent, prefix='.' + path.name + '.')
    with os.fdopen(fd, 'wb') as handle:
        handle.write(data)
    os.chmod(tmp, mode)
    os.chown(tmp, *owner)
    os.replace(tmp, path)


def read_env(text):
    env = {}
    for line in text.splitlines():
        if line.strip() and not line.lstrip().startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            env[key.strip()] = value.strip().strip('"').strip("'")
    return env


def caddy_env():
    env = os.environ.copy()
    env.update(read_env(CADDY_ENV.read_text()))
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
        raise SystemExit('Expected verify-jazz-auth.py next to this script; nothing changed')
    if not read_env(CADDY_ENV.read_text()).get('WORKBENCH_API_URL', '').startswith('https://'):
        raise SystemExit(f'Add WORKBENCH_API_URL=https://<workbench service URL> to {CADDY_ENV} first; nothing changed')
    if candidate == original:
        print('The /workbench/api route is already installed; verifying.')
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
    print('Workbench API route installed and verified; the Caddyfile kept its owner, group and mode.')


if __name__ == '__main__':
    main()
