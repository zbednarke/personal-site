"""Run as root after installing worker files, Python/NumPy and FFmpeg."""
import json,os,re,shutil,subprocess,time,urllib.request
from pathlib import Path
caddy=Path('/etc/caddy/Caddyfile');original=caddy.read_text()
if 'reverse_proxy 127.0.0.1:8768' not in original:raise SystemExit('Expected authenticated Jazz gateway missing; no routing changed')
user=json.loads(Path('/etc/jazz-auth.json').read_text())['User']
if not re.fullmatch(r'[a-zA-Z0-9_-]+',user):raise SystemExit('Unexpected Jazz identity')
Path('/etc/jazz-highlights-user.env').write_text('JAZZ_HIGHLIGHT_USER='+user+'\n')
for p in [Path('/srv/jazz-highlights'),Path('/srv/zachbednarke.com/films')]:
 p.mkdir(parents=True,exist_ok=True);shutil.chown(p,user='caddy',group='caddy')
lock=Path('/srv/zachbednarke.com/films/.publish.lock');lock.touch(exist_ok=True);shutil.chown(lock,user='caddy',group='caddy')
block='''handle_path /jazz/highlight-api/* {
        reverse_proxy 127.0.0.1:8769 {
            header_up X-Jazz-User {http.request.header.X-Jazz-User}
        }
    }
    '''
if '/jazz/highlight-api/*' not in original:
 needle='handle_path /jazz/api/* {'
 if original.count(needle)!=1:raise SystemExit('Unexpected API routing; no routing changed')
 updated=original.replace(needle,block+needle)
else:updated=original
candidate=Path('/etc/caddy/Caddyfile.highlights');candidate.write_text(updated)
env=os.environ.copy()
for line in Path('/etc/caddy/jazz.env').read_text().splitlines():
 if line.strip() and not line.lstrip().startswith('#'):
  k,v=line.split('=',1);env[k]=v.strip().strip('"').strip("'")
r=subprocess.run(['caddy','validate','--config',str(candidate),'--adapter','caddyfile'],env=env,capture_output=True)
if r.returncode:raise SystemExit('Caddy validation failed; existing configuration retained')
subprocess.run(['systemctl','daemon-reload'],check=True)
subprocess.run(['systemctl','enable','--now','jazz-highlights'],check=True)
for attempt in range(20):
 try:
  req=urllib.request.Request('http://127.0.0.1:8769/jobs',headers={'X-Jazz-User':user})
  with urllib.request.urlopen(req,timeout=1) as response:
   if response.status==200:break
 except OSError:time.sleep(.25)
else:raise SystemExit('Film service did not become healthy; existing routing retained')
backup=Path('/etc/caddy/Caddyfile.before-highlights');backup.write_text(original);backup.chmod(0o600)
shutil.copyfile(candidate,caddy)
r=subprocess.run(['systemctl','reload','caddy'],capture_output=True)
if r.returncode:
 caddy.write_text(original);subprocess.run(['systemctl','reload','caddy'],check=True);raise SystemExit('Reload failed; restored previous routing')
print('Hosted film service installed behind existing Jazz authentication')
