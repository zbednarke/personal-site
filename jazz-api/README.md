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
- `FFMPEG_PATH` (defaults to `ffmpeg`; the production image includes it)
- `PORT` (defaults to `8080`)

`JAZZ_ALLOW_INSECURE_LOCAL=1` is available only for local development.

## Clip Studio renders

Clip Studio render requests are authenticated, limited to 24 clips and ten
minutes, and stream directly from the private recording bucket through FFmpeg
back into the bucket. The output is a 1080p H.264 MP4 with two audio tracks:
default 320 kbps AAC for broadly compatible playback and the separate WAV
masters encoded as a second lossless 48 kHz ALAC track. Render objects live under the
`renders/` prefix and receive a GCS custom timestamp. Apply
`deploy/jazz-recordings-lifecycle.json` to the recordings bucket so those
temporary outputs are removed after one day. Cloud Run must allow long requests
for the synchronous MVP renderer. Deploy it with a 60-minute request timeout,
2 vCPU, 2 GiB memory, concurrency 1, and at least two instances so an encode
cannot starve ordinary API traffic:

```sh
gcloud run deploy jazz-api --source . --region us-central1 \
  --timeout 3600 --cpu 2 --memory 2Gi --concurrency 1 --max-instances 3
```

To try a feature branch against real data without changing the live site,
deploy a tagged revision that receives no traffic, then point the local pages at
it. Startup migrations run against the shared database, so only do this for
branches whose migrations are additive.

```sh
gcloud run deploy jazz-api --source . --region us-central1 --project parabolio-prod \
  --tag repertoire --no-traffic \
  --timeout 3600 --cpu 2 --memory 2Gi --concurrency 1 --max-instances 3
# from the repo root, with the tag URL printed by the deploy:
./dev.ps1 -ApiUrl https://repertoire---jazz-api-<hash>-uc.a.run.app
```

## Playback optimization

Browser-created WebM and fragmented MP4 recordings can require long scans before
their duration and seek index are usable. `cmd/repair-video-index` losslessly
remuxes those objects, moves MP4 metadata to the front, writes WebM cues, applies
a ten-minute private browser-cache policy, and keeps each original beside the
optimized file as `video.original-unindexed.*`. It verifies that the codecs and
size remain packet-preserving and refreshes GCS metadata in Postgres. It is
dry-run by default:

```sh
go run ./cmd/repair-video-index -all
go run ./cmd/repair-video-index -id RECORDING_UUID -apply
```

`Dockerfile.repair` and `cloudbuild.repair.yaml` package the same command for
the `jazz-video-index-repair` Cloud Run job so large recordings can be repaired
inside the bucket's region. Run the job periodically with `-all -apply`; it
selects only recordings whose `video_playback_optimized` flag is false.

## Rolling practice sections

The first load of a practice day copies the latest earlier day’s active recurring
sections, including custom titles, instructions, targets and order. It creates
fresh blocks without copying notes, recordings, timers or completion. Empty
plans remain empty. Date-specific appointments use `dayOnly: true` and are not
carried forward. Bootstrap mode `initialize` never overwrites saved settings;
mode `add` explicitly adds a section. Existing open tabs remain compatible.
Recurring sections keep their tune link (`tune_id`) when carried forward.

## Repertoire

`GET /v1/repertoire?today=YYYY-MM-DD&includeArchived=1` returns the user's tunes
with derived state (`deeplyLearned`, `practiceStatus`, last practiced date,
total and this-week practice, session and take counts), unlinked tune practice
and this week's total and jazz-track minutes. The first call per user seeds the
starter list and imports the legacy campaign roadmap stages once
(`repertoire_settings` marks it), so archived or swapped tunes never return.

- `POST /v1/repertoire/tunes` creates a tune; the id is the title slug with a
  `-2` suffix on collision. An archived slug returns 409 `{archived: true}` so the
  client can offer a restore.
- `PATCH /v1/repertoire/tunes/{tuneId}` needs `expectedRevision` (409 returns the
  current tune) and `clientMutationId` (retries are no-ops). Each changed field
  writes a `repertoire_events` row; `practiceBlockId` links it to a practice day.
- `GET /v1/repertoire/tunes/{tuneId}/history` groups linked blocks and takes by
  day, with change events and guide-tone drill stats.
- `PATCH /v1/repertoire/settings` sets `setTargetDate`.

Practice blocks link to tunes through `practice_blocks.tune_id`. Bootstrap and
`PATCH /v1/practice-blocks/{id}` accept `tuneId`; an unknown or archived tune is
422 except in `initialize` mode, where it is dropped so a stale curriculum file
cannot block a day. New takes default to their block's tune. Tune practice is
always derived from blocks (reconciled with their takes) and recordings and is
never copied. Milestones only change by explicit edits; practice can promote a
tune to learning, never to solid. Migration 024 backfills links on existing
blocks once, guarded by `jazz_backfills`.
