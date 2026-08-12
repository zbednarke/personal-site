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

The daily guide is stored as dated practice blocks within an active practice
session. Each block keeps its own instructions, autosaved notes, synchronized
recording-derived practice time, and up to twenty categorized recordings with
cloud-synced notes on each take. Live
takes include an on-page waveform and concert-pitch chromatic tuner. A compact
review area keeps recent session notes and playable recordings close to the
daily guide.

The live `/jazz/` route is protected by Caddy HTTP Basic Authentication.
`deploy/Caddyfile.jazz.example` documents the path matcher and privacy headers;
the live configuration reuses the existing Portal credential hash and forwards
authenticated API requests through a private gateway secret. Passwords, hashes,
and gateway credentials do not belong in this repository.

## Local Jazz development

Run `./dev.ps1` from PowerShell, then open
`http://localhost:4173/jazz/`. Localhost deliberately has no login prompt. The
development server reads the Jazz gateway credential from Google Secret Manager
at startup and proxies API calls to the production Jazz service as user `zach`.
This means local edits use the same PostgreSQL records and private GCS recording
bucket as the live page without exposing a database URL, cloud credential, or
gateway secret to browser JavaScript or saving one in the repository.
