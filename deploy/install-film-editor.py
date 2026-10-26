"""Install a staged film editor release on the existing VM, with rollback.

Run as root: python3 install-film-editor.py STAGING EXPECTED_CURRENT RELEASE_NAME
Staging contains tools/highlight plus the explicit frontend files below.
Dependencies: python3-pil, python3-numpy, ffmpeg, fonts-dejavu-core.
"""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time
import urllib.request

FRONTEND=['jazz/index.html','assets/jazz/jazz.css','assets/jazz/archive.js','assets/jazz/archive-utils.js','assets/jazz/clip-studio.js','assets/jazz/film-clips-model.js','assets/jazz/finished-films.js','assets/jazz/highlight-local.js','assets/jazz/film-edit-model.js','assets/jazz/film-editor.js','assets/jazz/film-editor.css','assets/jazz/library.js','assets/jazz/library.css','assets/jazz/library-resources.css']

def install(staging,expected,name):
    if not re.fullmatch(r'[a-zA-Z0-9-]+',name):raise SystemExit('Invalid release name')
    current=Path('/srv/zachbednarke.com/current');old=current.resolve()
    if str(old)!=expected:raise SystemExit('Frontend changed; inspect the new current release before deploying')
    worker=Path('/opt/jazz-highlights');backup=worker.with_name('jazz-highlights-before-'+name)
    release=current.parent/'releases'/name
    if release.exists() or backup.exists():raise SystemExit('Release already exists; choose a fresh name')
    user=json.loads(Path('/etc/jazz-auth.json').read_text())['User']
    def request(path):
        with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8769'+path,headers={'X-Jazz-User':user}),timeout=5) as r:return json.load(r)
    if any(j['phase'] not in ('complete','failed','cancelled') for j in request('/jobs')):raise SystemExit('A render is active; wait before deploying')
    subprocess.run(['/usr/bin/python3','-m','unittest','discover','-s',str(staging/'tools/highlight'),'-p','test_*.py'],check=True)
    shutil.copytree(old,release)
    for file in FRONTEND:shutil.copyfile(staging/file,release/file)
    shutil.copytree(worker,backup)
    subprocess.run(['systemctl','stop','jazz-highlights'],check=True)
    try:
        for file in (staging/'tools/highlight').iterdir():
            if file.is_file():shutil.copyfile(file,worker/file.name)
        subprocess.run(['systemctl','start','jazz-highlights'],check=True)
        healthy=False
        for _ in range(20):
            try:request('/edits');healthy=True;break
            except OSError:time.sleep(.25)
        if not healthy:raise RuntimeError('Editor service did not become healthy')
        temp=current.with_name('current-film-editor-next');temp.symlink_to(release);os.replace(temp,current)
        print('Installed '+name+'; previous frontend '+str(old)+'; worker backup '+str(backup))
    except Exception:
        subprocess.run(['systemctl','stop','jazz-highlights'],check=True)
        # Restore only executable code, retaining all persistent data outside /opt.
        shutil.rmtree(worker);shutil.copytree(backup,worker)
        subprocess.run(['systemctl','start','jazz-highlights'],check=True)
        raise

if __name__=='__main__':install(Path(sys.argv[1]),sys.argv[2],sys.argv[3])
