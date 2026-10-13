"""Package only the static files the live release does not already have.

  python3 deploy/package-static-release.py --live live.sha256 --out site.tar.gz PATH...

PATH... are the release paths (files or directories) as the full archive would
take them. --live is `sha256sum` output for the live release's copy of those
paths, read on the site VM; it may be empty or missing (first deploy, read
failed), and then every file is packaged.

publish-static-release.sh starts the new release as a copy of the live one and
extracts this archive over it, so leaving out a file whose bytes the live
release already has publishes exactly the same files as the full archive. Only
the upload shrinks: it goes through the IAP tunnel (deploy/upload-static-release.sh),
and a typical deploy changes a few small files. A file is left out
only when the live copy's SHA-256 is the same; anything else (new, changed,
not readable there, a symlink) is packaged.
"""
import argparse
import hashlib
import os
import re
import sys
import tarfile

LINE = re.compile(r'^([0-9a-f]{64}) [ *](.+)$')


def live_hashes(path):
    """{relative path: sha256} from sha256sum output; junk lines are ignored."""
    hashes = {}
    try:
        with open(path, encoding='utf-8', errors='replace') as f:
            for line in f:
                m = LINE.match(line.rstrip('\r\n'))
                if m and not m.group(2).startswith('\\'):  # escaped names: just ship them
                    hashes[os.path.normpath(m.group(2))] = m.group(1)
    except FileNotFoundError:
        pass
    return hashes


def release_files(paths):
    """Every file and symlink under paths, sorted, as relative paths."""
    files = []
    for path in paths:
        if os.path.islink(path) or os.path.isfile(path):
            files.append(os.path.normpath(path))
            continue
        if not os.path.isdir(path):
            raise SystemExit(f'missing release path: {path}')
        for root, dirs, names in os.walk(path):
            dirs.sort()
            for name in names:
                files.append(os.path.normpath(os.path.join(root, name)))
            # Symlinked directories are entries, not trees to walk.
            files.extend(os.path.normpath(os.path.join(root, d)) for d in dirs if os.path.islink(os.path.join(root, d)))
    return sorted(set(files))


def sha256(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def changed_files(paths, live):
    return [f for f in release_files(paths) if os.path.islink(f) or live.get(f) != sha256(f)]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('--live', required=True, help='sha256sum output of the live release (may be empty)')
    parser.add_argument('--out', required=True, help='archive to write (.tar.gz)')
    parser.add_argument('paths', nargs='+')
    args = parser.parse_args(argv)
    live = live_hashes(args.live)
    every = release_files(args.paths)
    changed = changed_files(args.paths, live)
    with tarfile.open(args.out, 'w:gz') as archive:
        for name in changed:
            archive.add(name, recursive=False)
    size = sum(os.path.getsize(f) for f in every if os.path.isfile(f))
    print(f'{len(changed)} of {len(every)} files packaged ({len(every) - len(changed)} already live); '
          f'archive {os.path.getsize(args.out)} bytes (all files {size} bytes uncompressed); '
          f'{len(live)} live hashes read')
    for name in changed[:50]:
        print('  ' + name)
    if len(changed) > 50:
        print(f'  ... and {len(changed) - 50} more')
    return 0


if __name__ == '__main__':
    sys.exit(main())
