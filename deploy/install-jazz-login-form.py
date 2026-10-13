"""Install the form login on an existing seven-day Jazz auth deployment."""
import os, shutil, subprocess, time, urllib.request, urllib.error
from pathlib import Path
caddy=Path('/etc/caddy/Caddyfile')
original=caddy.read_text()
marker='    route @jazz_private {'
route='    handle /jazz/sign-in {\n        reverse_proxy 127.0.0.1:8768\n    }\n'
assert marker in original, 'Expected existing authenticated route'
updated=original if 'handle /jazz/sign-in {' in original else original.replace(marker,route+marker,1)
env=os.environ.copy()
for line in Path('/etc/caddy/jazz.env').read_text().splitlines():
 if line.strip() and not line.lstrip().startswith('#'):
  key,value=line.split('=',1);env[key]=value.strip().strip('"').strip("'")
candidate=Path('/etc/caddy/Caddyfile.login-form');candidate.write_text(updated)
result=subprocess.run(['caddy','validate','--config',str(candidate),'--adapter','caddyfile'],env=env,capture_output=True)
assert result.returncode==0,'Invalid Caddy config; nothing changed'
binary=Path('/usr/local/bin/jazz-auth');backup=Path('/usr/local/bin/jazz-auth.before-form')
shutil.copyfile(binary,backup);backup.chmod(0o755)
Path('/etc/caddy/Caddyfile.before-form').write_text(original)
try:
 shutil.copyfile('/tmp/jazz-auth-form',str(binary)+'.next');Path(str(binary)+'.next').chmod(0o755);os.replace(str(binary)+'.next',binary)
 subprocess.run(['systemctl','restart','jazz-auth'],check=True)
 for attempt in range(30):
  try:
   with urllib.request.urlopen('http://127.0.0.1:8768/jazz/sign-in',timeout=1) as r:assert r.status==200
   break
  except OSError:time.sleep(.2)
 else:raise RuntimeError('Sign-in service failed health check')
 shutil.copyfile(candidate,caddy)
 subprocess.run(['systemctl','reload','caddy'],check=True,capture_output=True)
 with urllib.request.urlopen('https://zachbednarke.com/jazz/sign-in',timeout=20) as r:
  assert r.status==200 and b'autocomplete="current-password"' in r.read()
 req=urllib.request.Request('https://zachbednarke.com/jazz/films/index.json',headers={'Accept':'text/html','Sec-Fetch-Dest':'document'})
 with urllib.request.urlopen(req,timeout=20) as r:assert '/jazz/sign-in?next=' in r.url and r.status==200
 # Check existing private routes and cookies, without exposing credentials.
 subprocess.run(['python3',str(Path(__file__).with_name('verify-jazz-auth.py'))],check=True)
except Exception:
 shutil.copyfile(backup,str(binary)+'.rollback');Path(str(binary)+'.rollback').chmod(0o755);os.replace(str(binary)+'.rollback',binary)
 caddy.write_text(original)
 subprocess.run(['systemctl','restart','jazz-auth'],check=True)
 subprocess.run(['systemctl','reload','caddy'],check=True,capture_output=True)
 raise
print('Form sign-in deployed; private routes and existing sessions verified')
