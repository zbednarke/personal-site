"""Publish completed local films; retries are independent of the editing worker."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import time

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
REMOTE = "actual-server"
FLAGS = ["--project=actual-budget-zb", "--zone=us-west1-a", "--tunnel-through-iap", "--quiet"]

def run(args):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=1800,
                          creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0)).stdout

def save(path, value):
    tmp = path.with_suffix('.tmp')
    tmp.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding='utf-8')
    tmp.replace(path)

def metadata(job):
    jid = job['id']
    if not re.fullmatch(r'[a-f0-9-]{36}', jid):
        raise ValueError('Invalid job identity')
    date = job['date']
    if datetime.date.fromisoformat(date).isoformat() != date:
        raise ValueError('Invalid practice date')
    seconds = round(job['durationMs'] / 1000)
    if seconds <= 0:
        raise ValueError('Invalid duration')
    film_id = date + '-' + jid
    return dict(id=film_id, jobId=jid, date=date, title=job['project']['title'][:120],
                description=f"{len(job['project'].get('clips', []))} moments from the practice room, brought together in one film.", duration=f'{seconds//60}:{seconds%60:02d}',
                video=f'/jazz/films/{film_id}.mp4', poster=f'/jazz/films/{film_id}.jpg')

def checksum(path):
    digest = hashlib.sha256()
    with path.open('rb') as handle:
        for chunk in iter(lambda: handle.read(1024*1024), b''):
            digest.update(chunk)
    return digest.hexdigest()

def publish(folder, job):
    film = metadata(job)
    video = folder / 'highlight.mp4'
    poster = folder / 'poster.jpg'
    run(['ffmpeg', '-v', 'error', '-y', '-ss', '1', '-i', str(video), '-frames:v', '1', '-q:v', '3', str(poster)])
    files = {'video': video, 'poster': poster}
    manifest = dict(film=film, hashes={key: checksum(path) for key, path in files.items()})
    save(folder / 'publish-manifest.json', manifest)
    staging = '/tmp/jazz-film-' + job['id']
    cloud = shutil.which('gcloud')
    if not cloud:
        raise RuntimeError('Google Cloud CLI is unavailable')
    run([cloud, 'compute', 'ssh', REMOTE, *FLAGS, '--command', 'mkdir -p ' + staging])
    run([cloud, 'compute', 'scp', *map(str, [video, poster, folder / 'publish-manifest.json', HERE / 'publish_remote.py']), REMOTE + ':' + staging + '/', *FLAGS])
    run([cloud, 'compute', 'ssh', REMOTE, *FLAGS, '--command', 'sudo python3 ' + staging + '/publish_remote.py ' + staging])
    # Mirror the published assets for the local development site as well.
    local = REPO / 'jazz/films'
    local.mkdir(parents=True, exist_ok=True)
    for key, src in files.items():
        dest = local / Path(film[key]).name
        if not dest.exists():
            try:
                os.link(src, dest)
            except OSError:
                shutil.copyfile(src, dest)
    index = local / 'index.json'
    films = json.loads(index.read_text(encoding='utf-8')) if index.exists() else json.loads((REPO / 'assets/jazz/finished-films.json').read_text(encoding='utf-8'))
    save(index, [f for f in films if f['id'] != film['id']] + [film])
    save(folder / 'publication.json', dict(phase='published', date=film['date'], url='https://zachbednarke.com/jazz/#studio', film=film))

def scan(root):
    for status in sorted(root.glob('*/status.json')):
        folder = status.parent
        try:
            job = json.loads(status.read_text(encoding='utf-8'))
            receipt = folder / 'publication.json'
            prior = json.loads(receipt.read_text(encoding='utf-8')) if receipt.exists() else {}
            if job['phase'] != 'complete' or prior.get('phase') == 'published':
                continue
            if prior.get('retryAt', 0) > time.time():
                continue
            save(receipt, dict(phase='uploading', date=job['date']))
            publish(folder, job)
            print('Published film for ' + job['date'], flush=True)
        except Exception as error:
            save(folder / 'publication.json', dict(phase='failed', message=str(error)[-1000:], retryAt=time.time()+300))
            print('Publishing failed; retry in five minutes: ' + str(error)[-300:], flush=True)

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--data', default=str(REPO / '.local/highlights'))
    parser.add_argument('--once', action='store_true')
    args = parser.parse_args()
    # A loopback-only lock prevents duplicate watcher processes, including across restarts.
    lock = socket.socket()
    try:
        lock.bind(('127.0.0.1', 8766))
    except OSError:
        raise SystemExit('Film publisher is already running')
    while True:
        scan(Path(args.data))
        if args.once:
            break
        time.sleep(20)
