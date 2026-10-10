# Workbench, phase 1: talk and triage, on every device

An owner-only chat that lives inside the private pages (`/jazz/`, `/trumpets/`
and `/commonplace/`). It talks about the page you are on, triages half-formed
ideas into GitHub issues, Commonplace Moments or practice notes, reads and
writes site data, and shows CI, PR and deploy status as cards. It cannot edit
code yet; that comes in phase 2 (issue #81).

## Architecture

- **One image, two Cloud Run services.** The Workbench code lives in the
  `jazz-api` Go package (`jazz-api/workbench*.go`, migration
  `028_workbench.sql`) and is served by a second Cloud Run service,
  `workbench`, started from the same image with `WORKBENCH_MODE=1`. In that
  mode the binary serves only `/health` and `/v1/workbench/*`.
  - It is a separate service because the API runs one request per instance
    (`--concurrency 1`, for Clip Studio renders) and throttles CPU between
    requests. Workbench holds event streams open (`--concurrency 40`) and
    keeps its CPU between requests (`--no-cpu-throttling`), so the agent loop
    carries on after the request that started it.
  - It is the same package, so the site-data tools call the existing handlers
    in-process (`wbInvoke`): the same validation and side effects as the
    pages, without an HTTP hop. It shares Postgres, the private bucket, the
    gateway key and the `authenticate` middleware.
- **Gateway.** Caddy routes `/workbench/api/*` to the service with the same
  sign-in check, verified `X-Jazz-User` and gateway key as `/jazz/api`
  (`deploy/install-workbench-route.py`). With `WORKBENCH_OWNER_SUBJECT` set,
  the service also refuses any other signed-in user.
- **The server owns all state** (`wb_*` tables): threads, messages,
  attachments (in the private bucket under `workbench/`), synced drafts,
  approvals, push subscriptions, runs and spend. Browsers keep a cache and an
  offline outbox only.
- **Event log.** Every change is an event in `wb_events` with a per-thread id
  allocated under the thread's row lock in the same transaction, so ids are
  gapless and commit in order. `GET /threads/{id}/events` is a server-sent
  event stream that replays from `Last-Event-ID` (or `?after=`) and then
  follows live. Streams always read from Postgres; in-process wake-ups and
  `LISTEN/NOTIFY` (for other instances) only tell them to look, so a missed
  wake-up costs latency, never an event. A device loads a snapshot
  (`GET /threads/{id}`, including the text of a reply still streaming) and
  streams from its `lastEventId`.
  - Event types: `message.created|started|delta|reset|completed`,
    `tool.started|finished` (with optional cards), `approval.requested|resolved|result`,
    `draft.updated`, `run.started|finished`, `spend.updated`, `thread.updated`.
    Later phases add types (timeline steps, overlays, previews) and reuse
    `wb_runs` (`agent` = `managed`, `external_ref` = the session id) and
    `wb_approvals` unchanged.
- **The agent loop** (`workbench_agent.go`) is a manual loop over the Messages
  API with the official Go SDK (beta namespace): `claude-opus-5-5`, adaptive
  thinking, streaming, effort `low` (the sheet's **Deep work** chip raises the
  next message to `high`), server-side refusal fallbacks
  (`server-side-fallback-2026-07-01`, `fallbacks: "default"`), and prompt
  caching (fixed tools and system prompt with a breakpoint, plus automatic
  caching of the conversation). The Messages API transcript is stored
  append-only (`wb_api_turns`) so cached prefixes and thinking blocks replay
  byte for byte. One loop runs per thread (a lease with a heartbeat); a
  restarted instance marks a dead loop's run `interrupted`, closes any
  dangling tool call and answers waiting messages.
- **Tools** (`workbench_tools.go`): `triage_idea`, `github_list_issues`,
  `github_create_issue`, `github_comment`, `site_status`, `jazz_today`,
  `jazz_repertoire`, `jazz_add_practice_block`, `jazz_mark_tune`,
  `jazz_add_note`, `commonplace_capture`.
  - Reads run at once. Writes show a confirmation card on every device and
    run only when approved; the first device to answer wins (a conditional
    update), the others get `409` and the outcome. Low-risk captures (a
    Moment, a practice note) run immediately.
  - Tool output is wrapped in `<untrusted_data>`; the system prompt says it
    is data, never instructions. A proposal made after the model read GitHub
    content in the same turn says so on its card. Cards show the full text to
    be written. GitHub writes (and flagged proposals) can't be approved from a
    notification; it opens the page.
  - Replies are rendered from Markdown with `createElement`/`textContent`
    only (links: http(s) or same-origin paths), and the hosting pages send
    `script-src 'self'; object-src 'none'; base-uri 'self'`.
- **Spend.** Each response's `usage` (per hop, when a fallback served it) is
  priced and added to the thread, the run and the month. Before each call the
  cost is estimated (the previous prompt plus what was appended, at the uncached
  rate, and a reply allowance); a call that would cross
  `WORKBENCH_MONTHLY_CAP_USD` is not made, and turns stop with a `capped` notice.
- **Request limits.** Images are validated and downscaled on upload (at most
  1568 px, JPEG or PNG). Images older than the last six transcript turns are
  replaced once by a placeholder, thinking blocks after that edit are removed,
  and requests send `prefix_mismatch_behavior: "drop_block"` as a safety net.
  A history the API refuses with a non-retryable 4xx (or one still over 20 MB)
  marks the thread stuck, and the sheet offers a fresh thread.
- **Refusal fallbacks.** After a mid-output fallback, thinking and tool_use
  blocks before the last `fallback` block are omitted from the echoed turn,
  only later tool calls run, and the visible reply keeps the partial text.
- **Deploys.** On SIGTERM the service stops claiming turns, lets running ones
  finish for 7 s, then cancels them as `interrupted` with a resume note; startup
  and a minute-by-minute sweep re-answer interrupted turns and any idle thread
  with unanswered messages.
- **Voice.** Hold-to-talk records audio in the browser and uploads it as an
  attachment. The Messages API has no audio input, so the server stores the
  clip (playable in the thread) and the agent sees
  `[voice note attached, 12 s; stored, not transcribed]`.
- **Web Push** (VAPID): approvals go to every subscribed device, with
  Approve / Not now buttons answered by the service worker; "ping me when
  replies are ready" is opt-in per device; "Open on my other devices" pushes a
  deep link. Payloads carry no content.
- **Front end** (`assets/workbench/`, framework-free): a glass pill and sheet,
  a bottom sheet on phones, a split view on tablets and a side panel on
  desktops. Press `` ` `` to toggle. Each message carries the page context
  (page, view, selection, device, viewport, local date); a page can add more
  with `window.WorkbenchContext = () => ({ view, selection, app })`. The
  outbox lives in IndexedDB and replays with the same client ids, which the
  server deduplicates. Each app has its own installable PWA scope
  (`/<app>/manifest.webmanifest`, `/<app>/workbench-sw.js`).

## API (`/v1/workbench`, browser path `/workbench/api/v1/workbench`)

| Method and path | Purpose |
| --- | --- |
| `GET /threads`, `POST /threads`, `PATCH /threads/{id}` | List, create, rename or archive |
| `GET /threads/{id}` | Snapshot: messages, approvals, draft, spend, `lastEventId` |
| `GET /threads/{id}/events` | SSE, resumable with `Last-Event-ID` |
| `POST /threads/{id}/messages` | Send (idempotent on `clientId`), with `context`, `attachmentIds`, `effort` |
| `PUT /threads/{id}/draft` | Save the synced draft |
| `POST /threads/{id}/attachments` | Raw image or audio body, `X-Workbench-Upload: 1`, optional `X-Workbench-Client-Id` |
| `GET /attachments/{id}` | 302 to a short-lived signed URL |
| `POST /approvals/{id}` | `{decision: approve or reject, deviceId}`; `409` if already decided |
| `GET /spend`, `GET /status` | Month spend and cap; CI, PR and deploy status |
| `GET /push/key`, `PUT/DELETE /push/subscriptions/{device}` | Web Push |
| `POST /threads/{id}/handoff` | Push a deep link to the other devices |

## Configuration

| Variable | Source | Notes |
| --- | --- | --- |
| `WORKBENCH_MODE=1` | env | Serve Workbench only |
| `WORKBENCH_OWNER_SUBJECT` | env | The owner's `X-Jazz-User` value |
| `ANTHROPIC_API_KEY` | secret `prism-anthropic-api-key` | Without it, turns end with a clear error |
| `WORKBENCH_GITHUB_TOKEN` | secret `workbench-github-token` | Fine-grained, this repository only: Issues read and write; Metadata, Pull requests, Actions and Contents read. Without it, reads are unauthenticated and writes are refused |
| `WORKBENCH_VAPID_PRIVATE_KEY` | secret `workbench-vapid-private-key` | Without it there is no Web Push |
| `WORKBENCH_VAPID_PUBLIC_KEY` | env | Matches the private key |
| `WORKBENCH_VAPID_SUBJECT` | env, optional | Default `mailto:workbench@zachbednarke.com` |
| `WORKBENCH_MONTHLY_CAP_USD` | env, optional | Default `25` |
| `WORKBENCH_MODEL`, `WORKBENCH_GITHUB_REPO` | env, optional | Defaults `claude-opus-5-5`, `zbednarke/personal-site` |

Everything else (`DATABASE_URL`, `GATEWAY_KEY`, `GCS_BUCKET`,
`GCP_SERVICE_ACCOUNT`, Cloud SQL) is copied from `jazz-api`. No key is ever
committed.

## Deploy

After the PR merges and the normal deploy has shipped the API (migration 028
runs on its startup) and the static pages:

1. Create the optional secrets (Cloud Shell, project `parabolio-prod`):

   ```sh
   cd jazz-api && go run ./cmd/workbench-vapid -secret workbench-vapid-private-key -project parabolio-prod
   # prints WORKBENCH_VAPID_PUBLIC_KEY=...; the private key goes straight to Secret Manager
   printf %s "$FINE_GRAINED_TOKEN" | gcloud secrets create workbench-github-token --data-file=- --project parabolio-prod
   ```

2. Create the service from the API's image. It copies the API's service
   account, Cloud SQL and environment, grants the service account access to
   the new secrets, and prints every command first:

   ```sh
   python3 deploy/create-workbench-service.py --owner <jazz-auth User> --vapid-public <public key>          # dry run
   python3 deploy/create-workbench-service.py --owner <jazz-auth User> --vapid-public <public key> --apply
   ```

3. On the site VM, add `WORKBENCH_API_URL=<the service URL>` to
   `/etc/caddy/jazz.env`, then install the route. It keeps the Caddyfile's
   owner, group and mode, validates, reloads, verifies (including the
   Workbench API) and rolls back on any failure:

   ```sh
   flags=(--project actual-budget-zb --zone us-west1-a --tunnel-through-iap)
   gcloud compute scp "${flags[@]}" deploy/install-workbench-route.py deploy/verify-jazz-auth.py actual-server:/tmp/
   gcloud compute ssh actual-server "${flags[@]}" --command 'sudo python3 /tmp/install-workbench-route.py --dry-run && sudo python3 /tmp/install-workbench-route.py'
   ```

4. Set the repository variable `WORKBENCH_ENABLED=true`. From then on every
   deploy also ships the new image to `workbench` (keeping its settings).

## Local preview and tests

- `go test ./...` with `TRUMPETS_TEST_DATABASE_URL` covers the event log
  (gapless ids, resume), two simultaneous subscribers on one streamed reply,
  a mid-stream snapshot, first-wins approvals, outbox replay, the spend cap,
  triage and the site-data tools, owner and privacy checks, and recovery. The
  Messages API is a scripted `httptest` server.
- `node --test assets/workbench/*.test.js` covers the event fold, convergence
  across devices, drafts, the outbox and the Markdown renderer.
- `node tools/workbench/browser-check.cjs` starts an isolated preview
  (`TestWorkbenchBrowserPreview`: loopback, throwaway schema, fake model and
  GitHub) and checks 390, 768 and 1440 px with touch and pointer, two browser
  contexts on one thread, draft sync, a first-wins approval, the offline
  outbox, hold-to-talk with Chromium's fake microphone and a status card.
  Screenshots land in `docs/workbench/screenshots/`.
