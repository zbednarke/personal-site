# Seven-day Jazz login

The loopback `jazz-auth` service validates the existing bcrypt login and issues a signed `__Host-jazz-session` cookie. It lasts seven days from login, with Secure, HttpOnly, SameSite=Lax and host-only scope. Requests preserve the original expiry. The signing key stays in `/etc/jazz-auth.json`, readable only by root and the caddy group. Changing the key or password hash invalidates existing cookies. No passwords or session tokens are logged.

Caddy authenticates private Jazz pages, assets, films and API requests before dispatch, and overwrites the API identity header with the verified username. Authentication failures remain closed. Cross-site unsafe requests are rejected. The existing gateway secret remains server-only. Local development's independent gateway connection is unaffected.

Build `jazz-api/cmd/jazz-auth` for Linux, install the executable as `/usr/local/bin/jazz-auth` and the unit as `/etc/systemd/system/jazz-auth.service`, then place `install-jazz-auth.py` and `verify-jazz-auth.py` together and run the installer as root. It retains the existing login, validates the candidate Caddy config with its service environment, tests authenticated and anonymous access to all private routes with automatic rollback on failure, and keeps `/etc/caddy/Caddyfile.before-week-login` for rollback. Configuration changes are separate from static site releases.

Rollback: restore the backup Caddyfile and reload Caddy. Rotating the signing key logs out saved sessions. Browsers can still cache Basic credentials; clearing site data/password authorization may be needed to fully forget a device. Cookie-blocking/private browsing can shorten remembered login.

## Browser-compatible sign-in

`/jazz/sign-in` serves a normal password form through the loopback auth service. Unauthenticated document navigation redirects there with a validated, same-origin Jazz return path. Form POST requires the site's Origin and issues the existing seven-day cookie. API requests retain 401 responses and existing Basic authentication remains supported. Failed form login renders an error without a native authentication challenge. No passwords are stored in browser scripts or logs.

Build `cmd/jazz-auth` for Linux amd64, transfer it as `/tmp/jazz-auth-form`, then run `install-jazz-login-form.py` alongside `verify-jazz-auth.py` as root. The installer checks the existing routing, validates Caddy, backs up the binary/config, verifies document redirects and private routes, and rolls back on failure. Existing cookies and credentials are preserved. Users sign in once separately in each browser.
