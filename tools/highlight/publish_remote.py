"""Install verified assets before atomically advertising a film. Runs on the site VM."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sys


def install(staging, root=Path('/srv/zachbednarke.com/films'), seed=Path('/srv/zachbednarke.com/current/assets/jazz/finished-films.json')):
    data = json.loads((staging / 'publish-manifest.json').read_text(encoding='utf-8'))
    film = data['film']
    if not re.fullmatch(r'\d{4}-\d{2}-\d{2}-[a-f0-9-]{36}', film['id']):
        raise ValueError('Invalid film identity')
    sources = {'video': staging / 'highlight.mp4', 'poster': staging / 'poster.jpg'}
    for key, source in sources.items():
        extension = '.mp4' if key == 'video' else '.jpg'
        if film[key] != '/jazz/films/' + film['id'] + extension:
            raise ValueError('Unexpected asset path')
        with source.open('rb') as handle:
            digest = hashlib.sha256()
            for chunk in iter(lambda: handle.read(1024*1024), b''):
                digest.update(chunk)
        if digest.hexdigest() != data['hashes'][key]:
            raise ValueError('Incomplete or corrupt upload')
    root.mkdir(parents=True, exist_ok=True)
    with (root / '.publish.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        index = root / 'index.json'
        films = json.loads((index if index.exists() else seed).read_text(encoding='utf-8'))
        for key, source in sources.items():
            dest = root / Path(film[key]).name
            temp = dest.with_suffix(dest.suffix + '.tmp')
            shutil.copyfile(source, temp)
            temp.chmod(0o644)
            os.replace(temp, dest)
        films = [f for f in films if f['id'] != film['id']] + [film]
        temp = root / 'index.json.tmp'
        temp.write_text(json.dumps(films, ensure_ascii=False, indent=2), encoding='utf-8')
        temp.chmod(0o644)
        os.replace(temp, index)
    # Remove only this job's known staging files after successful publication.
    for name in ('highlight.mp4', 'poster.jpg', 'publish-manifest.json', 'publish_remote.py'):
        (staging / name).unlink(missing_ok=True)

if __name__ == '__main__':
    install(Path(sys.argv[1]))
