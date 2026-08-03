# Jazz API

Private Cloud Run service for The Jazz Project. It stores campaign state and an
append-only change history in the isolated `jazz_project` database on
`parabolio-db`, and brokers private browser recordings into a dedicated GCS
bucket. Practice sessions contain session notes and structured activities;
recordings reference their parent session and carry tune, skill focus, take,
format, duration, and listening notes.

The service trusts only requests carrying both headers inserted by the Caddy
gateway:

- `X-Jazz-User`
- `X-Jazz-Gateway-Key`

Runtime configuration:

- `DATABASE_URL`
- `GATEWAY_KEY`
- `GCS_BUCKET`
- `GCP_SERVICE_ACCOUNT`
- `PORT` (defaults to `8080`)

`JAZZ_ALLOW_INSECURE_LOCAL=1` is available only for local development.
