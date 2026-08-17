# Jazz API

Private Cloud Run service for The Jazz Project. It stores campaign state and an
append-only change history in the isolated `jazz_project` database on
`parabolio-db`, and brokers private browser recordings into a dedicated GCS
bucket. Practice sessions contain session notes and structured activities;
recordings reference their parent session and carry tune, skill focus, take,
format, duration, and listening notes. A video take is stored as one logical
record with two private assets: a browser-playable video and a separate
lossless 48 kHz / 24-bit WAV master. A take recorded through the browser's
live effects chain additionally stores a processed "fx" WAV asset tagged with
its preset; the dry master remains the primary audio object. All assets use
resumable GCS uploads and the take becomes playable only after every declared
asset has passed server-side verification.

Authenticated users can create one permanent opaque share URL per recording
asset. The public endpoint validates that bearer token and redirects to fresh
short-lived GCS access. The bucket therefore stays private while the user-facing
share URL does not expire.

The service trusts only requests carrying both headers inserted by the Caddy
gateway:

- `X-Jazz-User`
- `X-Jazz-Gateway-Key`

Runtime configuration:

- `DATABASE_URL`
- `GATEWAY_KEY`
- `GCS_BUCKET`
- `GCP_SERVICE_ACCOUNT`
- `PUBLIC_SHARE_BASE_URL` (defaults to `https://zachbednarke.com/jazz/share`)
- `PORT` (defaults to `8080`)

`JAZZ_ALLOW_INSECURE_LOCAL=1` is available only for local development.
