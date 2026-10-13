"""Verify hosted-film auth without logging cookies or gateway credentials."""
import base64,hashlib,hmac,json,time,urllib.request,urllib.error
from pathlib import Path
c=json.loads(Path('/etc/jazz-auth.json').read_text())
value=base64.urlsafe_b64encode(c['User'].encode()).decode().rstrip('=')+'.'+str(int(time.time())+300)
signature=base64.urlsafe_b64encode(hmac.new(c['Key'].encode(),(c['Hash']+'|'+value).encode(),hashlib.sha256).digest()).decode().rstrip('=')
cookie='__Host-jazz-session='+value+'.'+signature

def request(auth=False, data=None, origin='https://zachbednarke.com', spoof=False):
 headers={'Origin':origin}
 if auth:headers['Cookie']=cookie
 if spoof:headers['X-Jazz-User']=c['User']
 if data is not None:headers['Content-Type']='application/json'
 req=urllib.request.Request('https://zachbednarke.com/jazz/highlight-api/jobs',headers=headers,data=json.dumps(data).encode() if data is not None else None)
 try:
  with urllib.request.urlopen(req,timeout=30) as r:return r.status,r.read()
 except urllib.error.HTTPError as e:return e.code,e.read()
assert request()[0]==401
assert request(spoof=True)[0]==401
status,body=request(True);assert status==200,status;assert isinstance(json.loads(body),list)
assert request(True,{},'https://attacker.example')[0]==403
assert request(True,{'date':'2026-09-28','recordings':[]})[0]==400
assert request(True,{'date':'not-a-date','targetSeconds':120})[0]==400
print('PASS: anonymous/spoofed identity denied, authenticated status works, cross-site writes and browser source lists rejected')
