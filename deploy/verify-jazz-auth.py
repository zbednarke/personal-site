"""Verify private-path sign-in on the live site. Run as root (reads /etc/jazz-auth.json).

Exits non-zero on the first failed expectation; the installers roll back on that.
JAZZ_AUTH_CONFIG and JAZZ_VERIFY_ORIGIN override the defaults for local testing.
"""
import base64, hashlib, hmac, json, os, time, urllib.error, urllib.parse, urllib.request
from pathlib import Path

ORIGIN = os.environ.get('JAZZ_VERIFY_ORIGIN', 'https://zachbednarke.com')
c = json.loads(Path(os.environ.get('JAZZ_AUTH_CONFIG', '/etc/jazz-auth.json')).read_text())
google = bool(c.get('GoogleClientID'))
days = int(c.get('SessionDays') or 30)


def b64(data):
    return base64.urlsafe_b64encode(data).decode().rstrip('=')


def mac(*parts):
    return hmac.new(c['Key'].encode(), '|'.join(parts).encode(), hashlib.sha256).digest()


def token(exp):
    """Mint a session cookie exactly as jazz-auth does (see server.go)."""
    if google:
        method, binding = 'g', c['GoogleClientID']
        tag = b64(mac('jazz-email', c['AllowedEmails'][0].strip().lower()))[:22]
    else:
        method, binding, tag = 'p', c['Hash'], '-'
    value = '.'.join(['v2', b64(c['User'].encode()), method, tag, str(exp)])
    return value + '.' + b64(mac('jazz-session-v2', method, binding, value))


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


opener = urllib.request.build_opener(NoRedirect)


def request(path, cookie=None, method='GET', origin=None, headers=None):
    h = dict(headers or {})
    if cookie:
        h['Cookie'] = '__Host-jazz-session=' + cookie
    if origin:
        h['Origin'] = origin
    req = urllib.request.Request(ORIGIN + path, headers=h, method=method)
    try:
        with opener.open(req, timeout=30) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.headers, error.read()


NAVIGATE = {'Accept': 'text/html,application/xhtml+xml', 'Sec-Fetch-Mode': 'navigate', 'Sec-Fetch-Dest': 'document'}
API = {'Accept': 'application/json', 'Sec-Fetch-Mode': 'cors', 'Sec-Fetch-Dest': 'empty'}
valid = token(int(time.time()) + days * 86400 - 10)

PAGES = ['/jazz/', '/trumpets/', '/commonplace/']
PRIVATE = ['/jazz/', '/assets/jazz/clip-studio.js', '/jazz/films/index.json', '/jazz/api/v1/state',
           '/trumpets/', '/assets/trumpets/app.js', '/trumpets/api/v1/trumpets/listings', '/trumpets/api/v1/trumpets/profile',
           '/commonplace/', '/assets/commonplace/app.js', '/commonplace/api/v1/commonplace/moments', '/commonplace/api/v1/commonplace/space']

for path in PAGES:
    status, headers, _ = request(path, headers=NAVIGATE)
    location = headers.get('Location', '')
    assert status == 302 and location == '/auth/login?next=' + urllib.parse.quote(path, safe=''), (path, status, location)
    assert not headers.get('WWW-Authenticate'), path
print('PASS: anonymous page loads redirect to /auth/login')

for path in PRIVATE:
    for extra in ({}, API, {'Authorization': 'Basic ' + base64.b64encode(b'zach:guess').decode()}):
        status, headers, body = request(path, headers=extra)
        assert status == 401, (path, extra, status)
        assert not headers.get('WWW-Authenticate'), (path, 'WWW-Authenticate must never be sent')
        assert headers.get('Content-Type', '').startswith('application/json'), (path, headers.get('Content-Type'))
        assert json.loads(body) == {'error': 'signin_required', 'login': '/auth/login'}, (path, body)
print('PASS: anonymous API and asset calls get 401 JSON without a Basic challenge')

status, headers, body = request('/auth/login?next=%2Fjazz%2F', headers=NAVIGATE)
assert status == 200 and b'Sign in' in body, status
assert "default-src 'none'" in headers.get('Content-Security-Policy', ''), 'login page CSP'
if google:
    assert b'Sign in with Google' in body
    status, headers, _ = request('/auth/google/start?next=%2Fjazz%2F', headers=NAVIGATE)
    location = urllib.parse.urlsplit(headers.get('Location', ''))
    query = urllib.parse.parse_qs(location.query)
    assert status == 302 and location.netloc == 'accounts.google.com', (status, location.netloc)
    assert query['client_id'] == [c['GoogleClientID']] and query['code_challenge_method'] == ['S256'], 'authorization request'
    assert query['redirect_uri'] == [c.get('PublicOrigin', 'https://zachbednarke.com').rstrip('/') + '/auth/google/callback']
    assert '__Host-jazz-oauth=' in headers.get('Set-Cookie', ''), 'state cookie'
    status, headers, _ = request('/auth/google/callback?state=x&code=y', headers=NAVIGATE)
    assert status == 303 and headers.get('Location', '').startswith('/auth/login?error=expired'), (status, headers.get('Location'))
print('PASS: /auth/login is public and the Google flow starts with PKCE')

for path in PRIVATE:
    status, headers, _ = request(path, valid, headers=NAVIGATE if path in PAGES else API)
    assert status == 200, (path, status)
    cookie = headers.get('Set-Cookie', '')
    assert 'HttpOnly' in cookie and 'Secure' in cookie and 'SameSite=Lax' in cookie and 'Max-Age=' in cookie, (path, 'cookie policy')
    print('PASS: signed-in access', path)

assert request('/jazz/', valid + 'x', headers=NAVIGATE)[0] == 302
assert request('/jazz/api/v1/state', token(int(time.time()) - 1))[0] == 401
assert request('/jazz/api/v1/state', valid.replace('.g.', '.p.', 1) if google else valid.replace('.p.', '.g.', 1))[0] == 401
assert request('/jazz/api/v1/sync', valid, 'POST', 'https://attacker.example')[0] == 403
assert request('/trumpets/api/v1/trumpets/seed', valid, 'POST', 'https://attacker.example')[0] == 403
assert request('/commonplace/api/v1/commonplace/moments', valid, 'POST', 'https://attacker.example')[0] == 403
assert request('/commonplace/api/v1/commonplace/import/bundle', valid, 'POST', 'https://attacker.example')[0] == 403
assert request('/auth/logout', valid, 'POST', 'https://attacker.example')[0] == 403
print('PASS: tampered, expired and method-swapped cookies and cross-site writes denied')

assert request('/')[0] == 200
print('PASS: public home unchanged')
