# Private-path sign-in (jazz-auth)

`jazz-auth` is a small loopback service on `127.0.0.1:8768` that guards the
private paths of zachbednarke.com: `/jazz`, `/assets/jazz`, `/trumpets`,
`/assets/trumpets`, their `/…/api/*` proxies, `/jazz/films`, and (reserved)
`/commonplace` and `/assets/commonplace`. People sign in with Google. There is
no Basic Auth dialog any more.

## How it works

- **Caddy asks first.** Every private request goes to `jazz-auth` as
  `GET /check` (with `X-Forwarded-Method` and `X-Forwarded-Uri`). A valid
  session cookie returns 200 with `X-Jazz-User: <User>`; Caddy then overwrites
  the API identity header with that value. Otherwise `jazz-auth` answers the
  browser directly:
  - a page load (`Sec-Fetch-Mode: navigate`, or `Accept: text/html` when Fetch
    Metadata is absent) gets `302 /auth/login?next=<original path>`;
  - anything else (fetch, API, assets) gets
    `401 {"error":"signin_required","login":"/auth/login"}` with no
    `WWW-Authenticate` header. The Jazz and Trumpets pages load
    `assets/jazz/signin-guard.js`, which turns that response into a
    "Signed out. Sign in again" banner that returns to the same page. Offline
    outboxes and retry queues keep their data and retry after sign-in.
  - cross-site unsafe methods (POST/PUT/PATCH/DELETE from another origin) are
    rejected with 403, as before.
- **`/auth/*` is public** and proxied straight to `jazz-auth`:
  - `GET /auth/login?next=…`: the sign-in page (no scripts, no third-party
    requests, strict CSP);
  - `GET /auth/google/start`: OpenID Connect authorization-code flow with PKCE
    (S256), `state` and `nonce`, scopes `openid email`. The state, nonce and
    verifier live in a 10-minute, AES-GCM-encrypted
    `__Host-jazz-oauth` cookie (Secure, HttpOnly, SameSite=Lax);
  - `GET /auth/google/callback`: checks `state`, exchanges the code with the
    client secret and PKCE verifier, and validates the ID token: RS256 against
    Google's JWKS (cached per `Cache-Control`, refetched for an unknown `kid`),
    `iss`, `aud`, `exp`/`iat` (2-minute skew), `nonce`, `email_verified`, and the
    email against `AllowedEmails` (case-insensitive). It then issues the session
    cookie and returns to `next`, which must be a same-site path (anything
    else goes to `/jazz/`);
  - `GET /auth/logout` shows a confirm button; `POST /auth/logout`
    (same-origin only) clears the cookie;
  - `POST /auth/password`: only when `AllowPassword` is true (see below).
- **Sessions.** `__Host-jazz-session` (Secure, HttpOnly, SameSite=Lax,
  host-only) lasts `SessionDays` (default 30) from sign-in and is never
  extended. Its HMAC covers the user, the sign-in method, a keyed hash of the
  email, and the expiry, so:
  - rotating `Key` signs everyone out;
  - disabling a method (removing Google, or setting `AllowPassword` false) ends
    that method's sessions;
  - changing the Google client ID ends Google sessions, and changing the
    password hash ends password sessions;
  - removing an email from `AllowedEmails` signs that account out.
  Cookies from before Google sign-in (the old format) are not accepted, so
  everyone signs in once after the upgrade.
- **API identity is unchanged.** Whoever signs in, the API sees the configured
  `User` (for example `zach`), so existing data keys still match.
- **Logs** never contain secrets, codes, tokens, passwords or email addresses;
  an account appears only as the first 8 characters of a keyed hash.

## Configuration: `/etc/jazz-auth.json`

Owned `root:caddy`, mode `0640`.

| Key | Meaning |
| --- | --- |
| `User` | API identity forwarded as `X-Jazz-User`. Keep it as is. |
| `Key` | Session signing key (32+ characters). Rotating it signs everyone out. |
| `Hash` | bcrypt hash for the password fallback. Kept even when unused. |
| `GoogleClientID` | OAuth web client ID (`….apps.googleusercontent.com`). |
| `GoogleClientSecret` | OAuth client secret. Also stored in Secret Manager. |
| `AllowedEmails` | Google accounts allowed to sign in. |
| `LoginHint` | Optional. Account Google pre-selects; defaults to the only allowed email. |
| `PublicOrigin` | Default `https://zachbednarke.com`. The redirect URI is `<PublicOrigin>/auth/google/callback`. |
| `SessionDays` | Session lifetime, 1 to 400 days. Default 30. |
| `AllowPassword` | Default `false`. When true, the sign-in page shows a small "Use password" form (POST, same-origin, rate-limited to 5 failures per address and 30 overall per 15 minutes). Basic Auth headers are never accepted or requested. |

The service refuses to start if Google is half-configured (client ID, secret
and allowed emails are all required together), if no sign-in method is enabled,
or if `AllowPassword` is on without a valid hash. `jazz-auth -check-config`
validates a file without starting (the installer uses it with
`JAZZ_AUTH_CONFIG=<file>`).

## One-time Google setup (owner)

Do this in the Google Cloud project that hosts the site VM (`actual-budget-zb`).

1. Open **Google Auth Platform**
   (<https://console.cloud.google.com/auth/overview?project=actual-budget-zb>)
   and choose **Get started** if it has not been set up yet: app name
   (for example "Zach Bednarke"), your support email, audience **External**, and
   your contact email.
2. **Clients → Create client → Web application.** Name it "zachbednarke.com
   sign-in". Under **Authorized redirect URIs** add exactly
   `https://zachbednarke.com/auth/google/callback`. No JavaScript origins are
   needed. Create it, then copy the **Client ID** and **Client secret** (the
   secret may only be shown in full once).
3. **Data Access:** the app uses only `openid` and `email` (both non-sensitive,
   so no Google verification is needed). Adding them here is optional.
4. **Audience:** either add your Google account under **Test users**, or choose
   **Publish app**. With only non-sensitive scopes, publishing needs no review.
   Either way, only addresses in `AllowedEmails` can get in.
5. Store the secret in Secret Manager (it will not be shown on screen or kept in
   shell history):

   ```sh
   read -rs SECRET && printf %s "$SECRET" | gcloud secrets create jazz-google-client-secret \
     --project actual-budget-zb --replication-policy automatic --data-file=- && unset SECRET
   ```

## Install on the site VM

Prerequisite: `install-jazz-auth.py` has already run (it put `jazz-auth` in
front of the private paths). Then, from a checkout of this branch:

```sh
cd jazz-api && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ../deploy/jazz-auth ./cmd/jazz-auth && cd ..
flags=(--project actual-budget-zb --zone us-west1-a --tunnel-through-iap)
gcloud compute ssh actual-server "${flags[@]}" --command 'mkdir -p /tmp/google-sign-in'
gcloud compute scp "${flags[@]}" deploy/jazz-auth deploy/install-google-sign-in.py deploy/verify-jazz-auth.py actual-server:/tmp/google-sign-in/
gcloud secrets versions access latest --secret jazz-google-client-secret --project actual-budget-zb \
  | gcloud compute ssh actual-server "${flags[@]}" --command \
    'sudo python3 /tmp/google-sign-in/install-google-sign-in.py --client-id CLIENT_ID.apps.googleusercontent.com --allowed-email you@gmail.com'
```

The secret travels over stdin only (never argv, never a file on the VM). The
installer:

1. checks the new binary accepts the merged configuration (`-check-config`) and
   that Caddy accepts the new Caddyfile (`caddy validate` with Caddy's own
   environment) before changing anything;
2. merges `GoogleClientID`, `GoogleClientSecret`, `AllowedEmails`,
   `PublicOrigin`, `SessionDays` and `AllowPassword` (false unless
   `--allow-password`) into `/etc/jazz-auth.json`, keeping `User`, `Hash` and
   `Key` untouched;
3. adds the public `route /auth/*` before the `@jazz_private` check in the live
   Caddyfile and adds `/commonplace`, `/commonplace/*` and
   `/assets/commonplace/*` to the private matcher;
4. installs the binary, restarts `jazz-auth`, reloads Caddy and runs
   `verify-jazz-auth.py`; if any step fails it restores the previous config,
   binary and Caddyfile and reloads.

First-run backups stay at `/etc/caddy/Caddyfile.before-google-sign-in`,
`/etc/jazz-auth.json.before-google-sign-in` and
`/usr/local/bin/jazz-auth.before-google-sign-in`. Re-running is safe; with no
secret on stdin it keeps the stored one. `--session-days N`, `--login-hint` and
`--allow-password` are optional.

`verify-jazz-auth.py` (run as root) checks that anonymous page loads redirect to
`/auth/login`, anonymous API and asset calls get 401 JSON with no
`WWW-Authenticate`, Basic credentials are ignored, `/auth/login` is public, the
Google flow starts with PKCE, a valid cookie still reaches every private path,
tampered/expired/method-swapped cookies and cross-site writes are refused, and
the public home page is unchanged.

## Adding a private app

Add its page and asset paths to the `@jazz_private` matcher (see
`deploy/Caddyfile.jazz.example`) and its API proxy, forwarding
`X-Jazz-User {http.request.header.X-Jazz-User}`. Nothing in `jazz-auth` changes.
Load `/assets/jazz/signin-guard.js` before the app's scripts for the signed-out
banner, and add the paths to `PRIVATE` in `verify-jazz-auth.py`.

## Rollback and recovery

- Restore the three backups above, then
  `systemctl restart jazz-auth && systemctl reload caddy`.
- Locked out with Google unavailable: set `"AllowPassword": true` in
  `/etc/jazz-auth.json` (the hash is still there) and restart `jazz-auth`; the
  sign-in page then offers "Use password".
- Rotate the Google secret: add a new secret in Google Auth Platform, add a
  Secret Manager version, re-run the installer with it on stdin, then delete
  the old secret in Google.
- Sign everyone out: replace `Key` with a new random value and restart.
- Cookie-blocking or private browsing can shorten how long a sign-in lasts.
