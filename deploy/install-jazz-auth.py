"""Run as root on the existing site VM. Preserve the current login and API identity."""
from pathlib import Path
import base64, hashlib, hmac, json, os, re, secrets, shutil, subprocess, time, urllib.request, urllib.error
caddy=Path('/etc/caddy/Caddyfile')
original=caddy.read_text()
pattern=r'(?m)^\s*basic_auth @jazz_private bcrypt restricted \{\s*(\S+) (\S+)\s*\}'
match=re.search(pattern,original)
if not match: raise SystemExit('Expected Jazz login block was not found; no configuration changed')
config=Path('/etc/jazz-auth.json')
if not config.exists():
 config.write_text(json.dumps(dict(User=match[1],Hash=match[2],Key=secrets.token_urlsafe(48))))
 shutil.chown(config,user='root',group='caddy');config.chmod(0o640)
replacement="""
    route {
    route @jazz_private {
        reverse_proxy 127.0.0.1:8768 {
            method GET
            rewrite /check
            header_up X-Forwarded-Method {method}
            header_up X-Forwarded-Uri {uri}
            @authenticated status 2xx
            handle_response @authenticated {
                request_header X-Jazz-User {rp.header.X-Jazz-User}
                header +Set-Cookie {rp.header.Set-Cookie}
            }
        }
    }
"""
updated=original[:match.start()]+replacement+original[match.end():]
if updated.count('\n\tfile_server\n') != 1: raise SystemExit('Unexpected site routing; no configuration changed')
updated=updated.replace('\n\tfile_server\n', '\n\tfile_server\n\t}\n', 1)
updated=updated.replace('header_up X-Jazz-User {http.auth.user.id}','header_up X-Jazz-User {http.request.header.X-Jazz-User}')
backup=Path('/etc/caddy/Caddyfile.before-week-login')
if not backup.exists(): shutil.copyfile(caddy,backup);backup.chmod(0o600)
candidate=Path('/etc/caddy/Caddyfile.week-login');candidate.write_text(updated)
# Use exactly Caddy's runtime environment for validation; never print its values.
env=os.environ.copy()
for line in Path('/etc/caddy/jazz.env').read_text().splitlines():
 if line.strip() and not line.lstrip().startswith('#'):
  key,value=line.split('=',1);env[key]=value.strip().strip('"').strip("'")
result=subprocess.run(['caddy','validate','--config',str(candidate),'--adapter','caddyfile'],env=env,capture_output=True)
if result.returncode: raise SystemExit('Caddy validation failed; existing config retained')
subprocess.run(['systemctl','daemon-reload'],check=True)
subprocess.run(['systemctl','enable','--now','jazz-auth'],check=True)
for attempt in range(20):
 try:
  urllib.request.urlopen('http://127.0.0.1:8768/check',timeout=1)
 except urllib.error.HTTPError as error:
  if error.code==401:break
 except OSError: time.sleep(.2)
else: raise SystemExit('Auth service did not start; existing Caddy config retained')
shutil.copyfile(candidate,caddy)
result=subprocess.run(['systemctl','reload','caddy'],capture_output=True)
if result.returncode:
 shutil.copyfile(backup,caddy);subprocess.run(['systemctl','reload','caddy'],check=True)
 raise SystemExit('Reload failed; restored previous login')
verification=Path(__file__).with_name('verify-jazz-auth.py')
result=subprocess.run(['python3',str(verification)],capture_output=True)
if result.returncode:
 shutil.copyfile(backup,caddy);subprocess.run(['systemctl','reload','caddy'],check=True)
 raise SystemExit('Access verification failed; restored previous login')
print('Seven-day Jazz login installed and access verified; existing username and password preserved')
