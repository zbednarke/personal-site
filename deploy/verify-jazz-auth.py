import base64,hashlib,hmac,json,time,urllib.request,urllib.error
from pathlib import Path
c=json.loads(Path('/etc/jazz-auth.json').read_text())
def token(exp):
 value=base64.urlsafe_b64encode(c['User'].encode()).decode().rstrip('=')+'.'+str(exp)
 sig=base64.urlsafe_b64encode(hmac.new(c['Key'].encode(),(c['Hash']+'|'+value).encode(),hashlib.sha256).digest()).decode().rstrip('=')
 return value+'.'+sig
valid=token(int(time.time())+604790)
def request(path,cookie=None,method='GET',origin=None):
 headers={}
 if cookie:headers['Cookie']='__Host-jazz-session='+cookie
 if origin:headers['Origin']=origin
 req=urllib.request.Request('https://zachbednarke.com'+path,headers=headers,method=method)
 try:
  with urllib.request.urlopen(req,timeout=30) as response: return response.status,response.headers,response.read()
 except urllib.error.HTTPError as error:return error.code,error.headers,b''
for path in ['/jazz/','/assets/jazz/clip-studio.js','/jazz/films/index.json','/jazz/api/v1/state']:
 status,_,_=request(path);assert status==401,(path,status)
 status,headers,body=request(path,valid);assert status==200,(path,status)
 cookie=headers.get('Set-Cookie','')
 assert 'HttpOnly' in cookie and 'Secure' in cookie and 'SameSite=Lax' in cookie and 'Max-Age=' in cookie
 print('PASS: private access and cookie-only login',path)
assert request('/jazz/',valid+'x')[0]==401
assert request('/jazz/',token(int(time.time())-1))[0]==401
assert request('/jazz/api/v1/sync',valid,'POST','https://attacker.example')[0]==403
print('PASS: tampered/expired cookies and cross-site write denied')
assert request('/')[0]==200
print('PASS: public home unchanged')
