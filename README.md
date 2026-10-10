# zachbednarke.com

Personal site: [zachbednarke.com](https://zachbednarke.com).

The hero is my name written as a function on the sphere and rebuilt from
its spherical-harmonic expansion as you scroll: 8,281 coefficients
synthesized live in the browser with a stable Legendre recurrence,
checked against SciPy reference values on every page load, and rendered
as a dot lattice on a 2D canvas. No WebGL, no frameworks, no build step.

- `index.html`: the site
- `assets/zach/sh.js`: real spherical-harmonic synthesis in JS
- `assets/zach/widget.js`: the scroll-driven renderer
- `assets/zach/data.js`: coefficients and verification reference values
- `jazz/index.html`: The Jazz Project dashboard at `/jazz/`
- `assets/jazz/`: campaign data, interactions, styles, and social preview

The Jazz Project is also framework-free. Progress is stored privately in the
`jazz_project` PostgreSQL database on Parabolio, with browser storage retained
as an offline cache and retry queue. Progress can still be exported or imported
as JSON. The page tracks practice time, skill-tree levels, repertoire stages,
scene milestones, and real-world boss fights.

`jazz-api/` contains the independent Cloud Run service, database migration, and
tests. It also brokers browser audio recordings into a dedicated private GCS
bucket. Recording metadata stays in PostgreSQL; audio bytes stay in object
storage. Practice sessions group session-wide notes, structured off-mic
activities, and their associated recordings. New browser takes are captured as
48 kHz / 24-bit mono lossless WAV files.

The recordings bucket remains private. Explicit per-asset share actions create
permanent opaque bearer links on `zachbednarke.com`; each visit resolves through
the API to fresh short-lived storage access, so shared URLs stay stable without
publishing the rest of the archive.

The daily guide is stored as dated practice blocks within an active practice
session. Each block keeps its own instructions, autosaved notes, synchronized
recording-derived practice time, and up to twenty categorized recordings with
cloud-synced notes on each take. Live
takes include an on-page waveform and concert-pitch chromatic tuner. A compact
review area keeps recent session notes and playable recordings close to the
daily guide.

The Guide Tone Reflex tool uses the selected browser microphone to grade thirds
and sevenths against a chord-only Blue Bossa roadmap. It supports written pitch
for B-flat trumpet or concert pitch, learn-at-your-own-pace and tempo-following
modes, and synchronized drill attempts. Time in the tool is attributed to the
corresponding daily practice block and appears in the normal session and archive
totals.

Repertoire (`/jazz/#repertoire`) tracks the set list: ten ballads learned
deeply (melody by ear in two keys, lyrics, one transcription), upbeat set tunes
and current pop, with goal progress, pace toward the spring target and a
"could I hold a set tonight" check. Tunes link to practice sections, so time,
notes and takes on a linked section become the tune's history without manual
entry, and Practice now adds a linked section to today's plan.

The private paths (`/jazz/`, `/trumpets/`, their assets and APIs, and the
reserved `/commonplace/`) use Sign in with Google. Caddy asks the loopback
`jazz-auth` service (`jazz-api/cmd/jazz-auth`) to check each request: a signed
session cookie (30 days by default) passes, a signed-out page load goes to
`/auth/login`, and a signed-out API call gets a 401 JSON response that the pages
turn into a "Signed out. Sign in again" banner; there is no Basic Auth dialog.
Only the Google accounts listed in the server's allowlist get in, and the API
still sees the same single user, so existing data is unchanged. A password form
exists as an off-by-default fallback. `deploy/jazz-auth.md` covers the design,
the one-time Google setup and the installer; `deploy/Caddyfile.jazz.example`
documents the routes and privacy headers. Client secrets, password hashes,
signing keys and gateway credentials do not belong in this repository.

## Local Jazz development

Run `./dev.ps1` from PowerShell, then open
`http://localhost:4173/jazz/`. Localhost deliberately has no login prompt. The
development server reads the Jazz gateway credential from Google Secret Manager
at startup and proxies API calls to the production Jazz service as user `zach`.
This means local edits use the same PostgreSQL records and private GCS recording
bucket as the live page without exposing a database URL, cloud credential, or
gateway secret to browser JavaScript or saving one in the repository.

## Private trumpet research

`/trumpets/` is the Trumpet Observatory: accumulated listings, daily signals,
price/status history, source coverage and private ratings/notes/preferences.
It reuses the Jazz Go/Postgres/gateway service. The daily runner searches across
specialist and international sources and rechecks active offers; the existing
ChatGPT search can read the private profile and submit verified daily reports.

The **Horn inspiration** board (`/trumpets/#inspiration`, linked from `/jazz/`)
captures trumpet links, screenshots and photos in a paste, privately.

See [Trumpets setup, API, privacy, tests and screenshots](docs/trumpets/README.md).
`./dev.ps1` also serves `http://localhost:4173/trumpets/` through the existing private
production proxy. Deployment must protect the Trumpets page/assets/API in Caddy;
the daily workflow needs server-side machine auth and runner secrets before enabling.

## Tests

The browser code uses Node's built-in test runner, so there is no
`package.json` or install step (Node 22+):

```sh
node --test assets/jazz/*.test.js assets/trumpets/*.test.js
cd jazz-api && go test ./...
```

Database-backed Go tests run when `TRUMPETS_TEST_DATABASE_URL` /
`JAZZ_LAYOUT_TEST_DATABASE_URL` point at a disposable Postgres. CI
(`.github/workflows/trumpets-tests.yml`) runs all of the above on every pull
request and push to `main`, plus the trumpet adapter and browser checks.
