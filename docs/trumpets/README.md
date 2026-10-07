# Private Trumpet Observatory

`/trumpets/` is a framework-free board backed by the existing Go `jazz-api`
service and PostgreSQL database. The database, rather than a conversation, is
its accumulated market memory. Jazz continues to use its existing routes.

## What is implemented

- Today shows meaningful new listings, newly discovered offers, price drops,
  rediscoveries and status changes occurring since midnight **UTC**. All tracked
  includes sold, removed, stale and acquired instruments. History is retained.
- Cards show photos when verified, maker/model, price/currency/shipping, seller,
  source, status, rationale, tags, rating, interest, favorites and private notes.
  The instrument-study illustration is a placeholder, not a listing photograph.
- Filters compose across maker, source, status, finish/features text, interest,
  favorites and active-only. Sorting includes percentage price drop and oldest
  unchecked. Prices sort **within currency groups**; no exchange rate is guessed.
- Feedback belongs to a physical horn and is shared across confirmed relists.
  Explicit liked/disliked attributes and ratings inform the baseline runner's
  ranking. Freeform notes and examples are available to a more capable search
  client, including the existing ChatGPT workflow, through the private profile.
- Source coverage reports checked, failed and skipped sources. Unreported catalog
  sources remain visibly unreported. A checked source means its search results
  were inspected, **not** that every inventory page was exhaustively crawled.
- A daily GitHub workflow runs broad search, deliberately searches for new sources,
  revalidates all active offers, submits observations and records coverage. It is
  disabled until deployment, secrets and the repository variable are configured.
  No remote deployment or changes to an existing ChatGPT task are made by this PR.

## Data and dedupe

Migration `021_trumpet_board.sql` adds instruments (`trumpet_horns`), offers
(`trumpet_listings`), horn feedback, runs, source checks, observations and events.
Core ownership, URL/source identities, money, status and timestamps are relational;
variable technical specifications, tags/images and attribute lists use JSONB.
Composite foreign keys enforce ownership between related records. Startup migrations
remain replayable, matching the existing service's migration convention.

URLs normalize scheme/host, trailing slash, fragment and common tracking parameters
while preserving meaningful query parameters. `(owner, canonical URL)` and
`(owner, source, source listing ID)` identify an offer. A maker and serial number
identify a physical horn across sources. A verified `hornId` can explicitly link
an instrument without a serial. Matching photos or distinctive provenance on the
same maker/model produce **possible relist suggestions**, without automatic merging.
Conflicting serial numbers never become fuzzy matches. Exact historical seed models
can attach to their first compatible verified offer; later different horns remain
separate. Price and status changes are server-detected, never trusted alert labels.

Ingestion is transactional and serialized per owner. A report's `externalId` is an
idempotency key: a replay returns its original run/status without adding history.
Use a new ID for additional work, including a corrected partial report. Observations
preserve daily unchanged prices too. The first-seen timestamp never resets.
A `combined`/`recheck` run with active instruments still unchecked that UTC day is
recorded **partial**, even if the client claimed success. Failed/ambiguous checks
preserve prior facts and leave instruments due. The board shows stale references
and all last-check dates; the runner never treats a blocked/login page as “sold”.

Acquisition is an instrument-level exclusion: it applies to all confirmed offers,
removes old events from Today's alerts and survives subsequent ingestion or feedback
reset. Acquired data remains available for comparisons, provenance, setup or resale.
The purchased Taylor Chicago 46 II / Harrelson-modified build is seeded acquired;
the same recognizable build is also excluded during ingestion when its serial is
unknown. Ordinary Taylor Chicago II instruments are not excluded by that rule.

## Historical seeds

The first empty-board visit submits an idempotent authenticated `POST /seed`.
Twenty-one references include the requested bought Taylor, 1948 Olds #27907,
MUSE, upswept Taylor, Adams A4, Lawler C7, OIRAM II, Dorotea, David Castro Bravura,
Yamaha 921X #002, gold B5, LOTUS Solo Max, Feroce, Faddis bundle, HC1, Meha,
Opera Premiere, Calicchio/Van Laar historical placeholders, GERDT and Concept TT.

Unknown URLs, sellers, prices, dates and photos stay unknown. References start
stale, with no fabricated last-check time or fresh-listing event. The Calicchio
and unusual Van Laar placeholders are explicitly incomplete, not live offers.
Seeds never overwrite saved feedback or previously verified offers. Searches can
supply their seed listing ID to enrich a reference, or add a separate verified offer
with a shared physical horn identity. Use `GET /machine/listings` for that comparison.

## Authentication and deployment

1. Build/deploy `jazz-api` with the existing Cloud Run process. Startup applies
   migration 021 to the existing PostgreSQL database; no new database or GCS bucket
   is needed. Back up the database through the usual deployment process.
2. Expand the site's existing **private path matcher** to include `/trumpets`,
   `/trumpets/*` and `/assets/trumpets/*`, **before** serving those files. Add the
   `/trumpets/api/*` reverse proxy from `deploy/Caddyfile.jazz.example`.
   Reuse the existing authenticated identity and gateway secret; overwrite client
   identity headers at the gateway. Validate Caddy before reload.
3. Preserve whichever existing login is deployed. The example uses Basic Auth;
   the repository also supports `jazz-auth`'s seven-day cookie. For the cookie
   deployment retain its existing `route @jazz_private` authentication gate and
   forward `X-Jazz-User {http.request.header.X-Jazz-User}` as on Jazz, rather than
   replacing it with a new Basic Auth gate. The expanded matcher includes Trumpets
   in both authentication and privacy headers. `deploy/verify-jazz-auth.py` now
   checks Trumpets too. Do not rerun the one-time login installer on an already
   installed cookie deployment merely to add this route.
4. After validating and reloading the private routes, publish `trumpets/` and
   `assets/trumpets/` in the existing static release. Switch the release symlink
   atomically and keep the previous release and Caddy configuration for rollback.
5. Generate a **random token of at least 32 bytes**, store the plaintext only in
   Secret Manager / the runner's secret store, and compute its SHA-256 hex digest.
   Configure **Cloud Run** with `TRUMPETS_MACHINE_TOKEN_SHA256` (the digest) and
   `TRUMPETS_MACHINE_USER` (the same verified login subject, e.g. `zach`). The
   machine API fails closed if either is missing. Its owner is fixed server-side;
   a supplied username header cannot select another user's notes. Rotate by replacing
   the token in the client and the digest in Cloud Run. Never commit either a token
   file, private profile snapshot, search report with feedback, or a cloud credential.
6. In GitHub Actions configure secrets `TRUMPETS_API_URL` (Cloud Run service base
   URL), `TRUMPETS_MACHINE_TOKEN` (plaintext **secret**) and either
   `BRAVE_SEARCH_API_KEY` or `OPENAI_API_KEY`.
   Set repository variable `TRUMPETS_DAILY_ENABLED=true` only after verification.
   The workflow runs at **13:17 UTC daily** and supports manual dispatch. Its log
   contains run ID/status/counts only, never the private profile/notes. No feedback
   is uploaded as an artifact. Check provider quotas and allow the required destinations
   if your runner has an outbound allowlist.
7. Verify anonymous page, assets and API/profile requests return 401; authenticated
   requests work; direct Cloud Run browser routes reject absent gateway headers;
   machine routes reject missing/wrong bearer tokens; cross-site writes return 403.

The API returns `private, no-store`, `noindex` and `no-referrer`; no CORS grants
are added. Browser data is not copied to localStorage, URL query parameters or
analytics. Notes render as text, never HTML. No private listing/profile is exposed
through the service's existing public recording-share routes. Static source code and
placeholder illustrations contain no private feedback.

## Search clients and daily revalidation

The baseline runner uses a Brave Search API query for every configured source group
plus deliberate new-source discovery. Sources include specialist dealers, Japanese
and European shops, auctions, regional shops, maker/demo inventory and credible
private offers; Reverb is only one group. Every active offer due that UTC day is
fetched separately, even if it did not occur in fresh search results.

If no Brave key is configured, `OPENAI_API_KEY` enables OpenAI web search through
the Responses API (default model `gpt-4.1-mini`, configurable with
`TRUMPETS_SEARCH_MODEL`). Only URLs from completed web-tool search sources are
used; generated prose is never taken as listing evidence. Dealer searches filter
returned URLs to the requested domains. Private notes are not sent to the search
provider. Both providers feed the same source-page verification, normalization,
dedupe and feedback scoring pipeline.
Temporary OpenAI HTTP/rate-limit/network errors get at most three attempts with
bounded backoff. Authentication failures are not retried. Exhausted failures remain
visible as partial coverage, never invented results. Progress logs contain only
source name, status and counts.

### VM-hosted daily job

When GitHub Actions secret administration is unavailable, the same runner can run
on the existing Caddy VM. Install it as `/opt/trumpets/runner.py`; put the service
and timer from `deploy/trumpets-research.*` in `/etc/systemd/system/`. Create
`/etc/trumpets/runner.env` owned by root with mode **0600**, containing
`TRUMPETS_API_URL`, `TRUMPETS_MACHINE_TOKEN`, and the selected provider's API key.
Obtain credentials through Secret Manager; never put this file in a static release.

The service runs as a dynamically allocated unprivileged user, with a read-only
filesystem and private temporary directory. The timer runs at **13:17 UTC** with
persistent catch-up after downtime. Enable with
`systemctl enable --now trumpets-research.timer`, and run the first search with
`systemctl start trumpets-research.service`. A partial report exits 2 and is
retained in the board, leaving unsupported active listings due for revalidation.
Use `journalctl -u trumpets-research.service` for run ID/status/counts only.
Enable **one** scheduler; keep `TRUMPETS_DAILY_ENABLED` unset for VM scheduling.

This conservative adapter ingests **unambiguous individual Product JSON-LD offers**.
It rejects aggregate prices, ordinary Bach/Yamaha without exceptional evidence,
non-trumpets and obvious non-Bb models; it has no warm/dark sound filter. It scores
priority makers/features and the owner's favored/disliked attributes. It does not
claim to understand freeform notes or exhaust websites without structured offers.
Unsupported/private/login/anti-bot pages and denied requests are reported; no
availability or price is invented. HTTP 404/410 becomes removed, explicit structured
sold/out-of-stock becomes sold, and a returning active offer becomes rediscovered.
Published dates, when supplied by a source, distinguish new listings from newly
found old offers. URL fetches restrict public HTTPS and reject private-network
addresses and unsafe redirects. Search result pages can require a specialized
adapter or human/ChatGPT verification, which the same ingestion protocol accepts.

Local runner setup (values come from a secret store, not a committed env file):

```sh
python tools/trumpets/runner.py
python tools/trumpets/runner.py --prepare /tmp/private-trumpet-context.json
python tools/trumpets/runner.py --report /tmp/verified-trumpet-run.json
```

`--prepare` writes profile, all tracked records and the due queue with mode 0600.
Keep that file private and remove it after use. Give the existing ChatGPT daily search
workflow access to the authenticated profile/all-tracked/due endpoints, then submit
its verified daily report. This replaces reliance on remembered conversation history.
No access to previous chat contents or its current scheduled task was assumed.

Machine endpoints under the Cloud Run base URL, using `Authorization: Bearer …`:

| Endpoint                            | Purpose                                                                                           |
| ----------------------------------- | ------------------------------------------------------------------------------------------------- |
| `GET /v1/trumpets/machine/profile`  | Ratings, notes, liked/disliked attributes, examples, maker/source priorities, acquired exclusions |
| `GET /v1/trumpets/machine/listings` | All tracked offers/history, today's meaningful events, latest coverage and successful runs        |
| `GET /v1/trumpets/machine/due`      | Active/stale offers not verified that UTC day; acquired instruments excluded                      |
| `POST /v1/trumpets/machine/runs`    | Atomic verified findings/rechecks and source coverage; idempotent external ID                     |

Browser endpoints use the existing gateway auth under `/trumpets/api/v1/trumpets/`:
`GET listings`, `GET profile`, `POST seed` with `{}`, and
`PUT listings/{id}/feedback` (full feedback replacement, nullable 1–5 rating).
`DELETE listings/{id}/feedback` resets ratings/notes/preferences/favorite for the
shared physical horn; it does not delete history or reverse acquisition. Writes
require `Content-Type: application/json`; the existing cookie gate and API origin
checks reject cross-site writes. Every query and mutation is owner-scoped.

Example normalized machine report (fictional values; use verified listing facts):

```json
{
  "externalId": "chatgpt-2026-10-05-01",
  "kind": "combined",
  "status": "partial",
  "error": "One active dealer page was blocked; left due for recheck.",
  "sources": [
    {
      "source": "Independent dealer",
      "status": "checked",
      "candidates": 1,
      "note": "Offer verified."
    },
    {
      "source": "Reverb",
      "status": "skipped",
      "candidates": 0,
      "note": "Unavailable this run."
    }
  ],
  "listings": [
    {
      "maker": "Lawler",
      "model": "C7",
      "serialNumber": "",
      "title": "Lawler C7 Bb trumpet",
      "description": "Source-grounded description",
      "url": "https://example.com/verified-offer",
      "source": "Independent dealer",
      "sourceListingId": "offer-123",
      "seller": "Verified seller",
      "location": "",
      "price": 3000,
      "currency": "USD",
      "shipping": null,
      "postedAt": null,
      "status": "active",
      "discoveryType": "newly discovered",
      "images": [],
      "searchScore": 85,
      "searchRationale": "Verified interesting configuration",
      "details": { "finish": "raw brass", "notableFeatures": ["custom bell"] },
      "tags": ["raw brass"],
      "evidence": "Offer page checked today"
    }
  ]
}
```

For an existing offer include its `id` and full normalized fields from the board/due
response; the server detects changes. Do not send read-only `firstSeen`, `feedback`
or history properties as candidate input. Known current URLs/source IDs also dedupe
without an ID. `new listing`, `newly discovered` and `rediscovered` are ingestion
classifications; `price drop` and `status change` are server-derived events.
Use `partial`/`failed` honestly and record failed/skipped sources explicitly.

## Tests and visual verification

```sh
cd jazz-api
TRUMPETS_TEST_DATABASE_URL='postgres://.../disposable' \
JAZZ_LAYOUT_TEST_DATABASE_URL='postgres://.../disposable' go test ./...
cd ..
node --test assets/jazz/*.test.js assets/trumpets/*.test.js
python -m unittest discover -s tools/trumpets -p 'test_*.py'
```

Database tests create/drop isolated random schemas. They apply all migrations twice,
exercise report replay, URL and source-ID dedupe, cross-source serial identity,
feedback CRUD and ownership, price/status histories, bought-horn exclusion and
incomplete daily rechecks. Unit tests cover auth, profile construction, URL shaping,
ranking/filtering, source adapters and unsupported/ambiguous observations.

With Playwright installed and a disposable PostgreSQL URL, run:

```sh
TRUMPETS_TEST_DATABASE_URL='postgres://.../disposable' \
CHROMIUM_PATH=/path/to/chromium node tools/trumpets/browser-check.cjs
```

This starts the test-only Go preview on loopback, verifies real API feedback
persistence after reload, ratings, filters and favorites, checks 320/390/768/1440px layouts, 44px phone touch targets, collapsible mobile
filters and notes-dialog/page horizontal overflow and JS errors, captures screenshots, then deletes its schema.
`TRUMPETS_GO` can select a local Go executable. Test fixtures are clearly labeled;
their prices, seller and illustrated images are synthetic, never market evidence.
The regular `dev.ps1` now serves `/trumpets/` with the existing **production** data
proxy as well as Jazz; use the isolated preview for tests.

[Desktop Today](screenshots/today.png) · [All tracked](screenshots/all-tracked.png) ·
[Mobile](screenshots/mobile.png) · [Mobile notes](screenshots/mobile-notes.png)


## Expanded discovery and candidate review

Candidates accumulates worthwhile search leads when a dealer blocks access or lacks
an unambiguous offer. These are explicitly unverified: no invented price, stock,
posted date, serial number or photo, and no market observations or Today alerts.
Rating, notes and favorites work privately. Set interest to `pass` to dismiss a lead
and exclude it from automated rediscovery/rechecks. Select the `pass` interest
filter to revisit dismissed leads. Acquired leads remain accessible in All tracked. Leads are retried daily; an
unambiguous verified offer promotes the same row, preserving first seen, feedback
and URL identity. Search hints cannot downgrade verified price/status history.
Acquired horns remain excluded from new-listing alerts.

Runs search the 27 source groups, new sources and private offers, plus nine explicit
maker/model queries covering boutique makers and exceptional production models.
Those extra queries exclude Reverb to encourage other sources; its dedicated source
search remains enabled. Citation titles are accepted only for actual web-tool URLs.
Parsers accept individual Product JSON-LD offers, dealer product metadata with
explicit price/currency/stock, and Shopify product JSON with explicit variant stock
and cart currency. Ambiguous variant prices and related products are rejected.
JPY prices retain zero-decimal units. Unverified leads have a lower triage threshold
(45 versus 55 for verified active offers).

Discovery is bounded to 240 page inspections and 35 minutes after starting the run;
active listing rechecks happen first, then up to 40 queued leads. Remaining promising result URLs can still enter
the queue. Source coverage records actual checks and unsupported pages, rather than
claiming exhaustive inventory. Authenticated clients can consume all freeform feedback;
the automated runner uses structured preferences and passes without interpreting notes.

Deployment adds replay-safe migration `022_trumpet_candidates.sql`, API/UI changes
and the updated stdlib runner. No Caddy, Cloud Run environment or secret changes are
required. Deploy the API before the runner/UI to support `verificationState`.
The existing VM timer stays daily at 13:17 UTC; the expanded runner uses a versioned
daily idempotency key so it can run once on upgrade.
[Candidate queue screenshot](screenshots/candidates.png) uses synthetic data.
