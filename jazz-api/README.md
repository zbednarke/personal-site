# Jazz API

Private Cloud Run service for The Jazz Project. It stores campaign state and an
append-only change history in the isolated `jazz_project` database on
`parabolio-db`, and brokers private browser recordings into a dedicated GCS
bucket. Practice sessions contain session notes and structured activities;
recordings reference their parent session and carry tune, skill focus, take,
format, duration, and listening notes. A video take is stored as one logical
record with two private assets: a browser-playable video and a separate
lossless 48 kHz / 24-bit WAV master. A take recorded through the browser's
live effects chain may also carry a processed "fx" WAV asset tagged with its
preset id. All assets use resumable GCS uploads verified server-side. The take
becomes playable once its dry master (and video, if any) verifies; the FX mix
is optional, appears in listings, playback, downloads, and shares only after
its own upload verifies, and `DELETE /v1/recordings/{id}/fx` drops it (and
revokes its share link) without touching the dry master.

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
- `FFPROBE_PATH` (defaults to `ffprobe`, or the sibling of an absolute `FFMPEG_PATH`)
- `LONG_TONE_WORKERS` (defaults to `1`, accepts `0` through `4`)
- `CAPTION_WORKERS` (defaults to `1`, accepts `0` through `4`)
- `PORT` (defaults to `8080`)

`JAZZ_ALLOW_INSECURE_LOCAL=1` is available only for local development.

## Saved speech captions

Authenticated caption endpoints queue durable, source-versioned transcription
for each recording's lossless WAV master. `POST /v1/studio/days/{date}/captions`
queues every take in a day; `POST` and `GET /v1/recordings/{id}/captions` control
and read one take. Results are source-time caption segments, so Clip Studio can
filter them to a selected clip and keep them aligned through seeks and trims.

The browser records a 24-bit archival WAV, while Speech batch recognition needs
a supported input encoding. The worker therefore streams that master through
ffmpeg into a 16-bit FLAC sidecar whose name includes the source generation. It
reuses that compact sidecar on retries without altering the master or using
container disk. Only the current caption-analysis version is claimable, so an
upgrade cannot spend worker time finishing obsolete queued jobs.

The worker uses Speech-to-Text V2 batch recognition against the private GCS
sidecar and never sends signed object URLs to the browser. Production requires
`speech.googleapis.com`, `roles/speech.client` on the Jazz API service account,
and bucket read access for the Speech service agent. Failed work has three
bounded attempts; source replacement creates a new analysis identity without
overwriting prior results.

## Saved long-tone analysis

The authenticated long-tone endpoint queues durable analysis for the exact
audio or video object generation being played. A bounded worker pool claims
jobs with Postgres leases, streams the generation-pinned object through FFmpeg,
and writes one-minute trace chunks. Retries are bounded and lease tokens fence
late workers after a reclaim. Video decoding preserves its presentation
timeline; a video without an audio stream fails explicitly and never falls back
to the separately captured WAV.

`POST /v1/recordings/{id}/long-tone-analysis` accepts
`{"asset":"video"}` or `{"asset":"audio"}` and idempotently creates the
current-version job. `GET` on the same path takes `asset`; optional `startMs`
and `endMs` select at most two minutes and include one observation on either
side when available so seeks retain classifier context.

The default deployment runs one worker inside each API instance. Cloud Run must
use instance-based billing (CPU allocated outside requests), because queued work
continues after the enqueue response. Keep at least one warm instance and use
concurrency 1 for the current two-vCPU image:

```sh
gcloud run deploy jazz-api --source . --region us-central1 \
  --timeout 3600 --cpu 2 --memory 2Gi --concurrency 1 --min-instances 1 \
  --max-instances 3 --no-cpu-throttling --update-env-vars LONG_TONE_WORKERS=1
```

Set `LONG_TONE_WORKERS=0` only for API instances paired with a separately
managed worker deployment. Signed source URLs last five hours and decoding is
hard-limited to four hours plus shutdown overhead. Analysis errors do not alter
recording playback or recording state.

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
  --timeout 3600 --cpu 2 --memory 2Gi --concurrency 1 --min-instances 2 \
  --max-instances 3 --no-cpu-throttling --update-env-vars LONG_TONE_WORKERS=1
```

## Magic Film Cloud Run job

Magic Film is asynchronous and does not need a local server or an always-on
renderer. The API freezes the chosen day's eligible moments in Postgres and
invokes one Cloud Run Job execution. The worker builds bounded evidence, asks
the configured OpenAI Responses model for a strict edit plan, validates source
boundaries, renders with FFmpeg, and stores the MP4/poster privately in GCS.
The browser can close and later recover job status and finished films.

Create the worker once (dry-run first), then enable image updates in deploys:

```sh
# First create Secret Manager secret magic-film-openai-api-key.
python3 deploy/create-magic-film-job.py
python3 deploy/create-magic-film-job.py --apply
# Set repository variable MAGIC_FILM_ENABLED=true.
```

The job has no idle instance, uses one task with no retries, and is capped at
2 vCPU, 4 GiB and 45 minutes. The application permits one active film per user,
at most 60 clips, at most 15 minutes of evidence per candidate, and a film no
longer than the selected target plus 15 seconds. `MAGIC_FILM_MODEL` defaults to
`gpt-6-astra`; set it on `jazz-api` before setup if another Responses API model
should be used. The setup script grants the API service account access to the
OpenAI secret and invocation of only this job.

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
- `POST /v1/repertoire/tunes/{tuneId}/sheets` accepts a raw PDF (25 MB max) plus
  `X-Sheet-*` metadata and stores it in the private object bucket. `GET` and
  `DELETE /v1/repertoire/sheets/{sheetId}` open it through a short-lived signed
  URL or remove it. Tune responses include their `sheets` list. The seed library
  contains an original B♭ blues practice chart; copyrighted charts are only
  added from the owner's licensed or owned files.
- `PATCH /v1/repertoire/settings` sets `setTargetDate`.

Practice blocks link to tunes through `practice_blocks.tune_id`. Bootstrap and
`PATCH /v1/practice-blocks/{id}` accept `tuneId`; an unknown or archived tune is
422 except in `initialize` mode, where it is dropped so a stale curriculum file
cannot block a day. New takes default to their block's tune. Tune practice is
always derived from blocks (reconciled with their takes) and recordings and is
never copied. Milestones only change by explicit edits; practice can promote a
tune to learning, never to solid. Migration 024 backfills links on existing
blocks once, guarded by `jazz_backfills`.
