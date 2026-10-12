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

A check that was **attempted but failed** (HTTP 401/403/429 from a site that blocks
automated checks, a timeout, or a page without one verifiable offer) is reported in
the run's `checkFailures` with a short reason. The server records it on the listing
(`checkFailures` = consecutive failed UTC days, `lastCheckAttempt`, `lastCheckError`)
and counts the listing as checked for that day, so one unreachable listing no longer
pins every run to partial. After **3** consecutive failed days a verified offer
becomes **`unverifiable`**: its last verified price stays visible, it is never marked
sold, it stays due and is retried daily, and a later successful check restores it
(any verified observation clears the streak). `unverifiable` is server-assigned; a
report cannot submit it. Lead attempts are recorded too, which rotates the lead queue.

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
3. Sign-in is handled by `jazz-auth` (Sign in with Google; see
   `deploy/jazz-auth.md`). Keep its `route @jazz_private` check and forward
   `X-Jazz-User {http.request.header.X-Jazz-User}` as on Jazz. The matcher
   includes Trumpets in both sign-in and privacy headers, and
   `deploy/verify-jazz-auth.py` checks Trumpets too. Do not rerun the one-time
   installers merely to add a route.
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

The adaptive runner plans 36 distinct source domains per run, six rotating maker/model searches, four exploration searches and four deliberate new-source searches. The domain universe starts with 57 international dealer, maker, community, auction and classified domains, then grows from useful verified offers and unverified leads. Sources include specialist dealers, Japanese
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
retained in the board. Runs are partial only when coverage is really missing (a
search provider failure or the 35-minute budget); failed listing checks are recorded
per listing as described above, and reaching the 240-page discovery bound is a note.
The board's telemetry shows the **latest run** (time, status and first note) next to
the **last complete run**, and flags a run older than about a day as overdue.
Use `journalctl -u trumpets-research.service` for run ID/status/counts only.
Enable **one** scheduler; keep `TRUMPETS_DAILY_ENABLED` unset for VM scheduling.

**Daily fix list.** When a run starts, the runner registers it
(`POST /v1/trumpets/machine/fixlist/start` with its `externalId`), so the fix
list says the run is under way rather than missing. After submitting a run
that started between **13:00 and 16:00 UTC** (the timer's 13:17 run), or when
given `--trigger-fix-list`, it calls `POST /v1/trumpets/machine/fixlist` with
`{}` (same bearer token); a manual run earlier in the day sends nothing. The API then
sends the owner's daily fix list: what failed in the last 24 hours (this run's
status and notes, failed or skipped sources by reason, failed checks by domain
and reason, listings that became unverifiable, Reverb/eBay/OpenAI budget and
cap notes, failed Magic Film jobs, the API's own 5xx and slow routes) against
the 24 hours before, as a Workbench push and a message in the "Fix list"
thread (see [docs/workbench/README.md](../workbench/README.md#daily-fix-list)).
The call is idempotent per UTC day. A failed call prints one line and never
changes the run's exit code. If the run never gets that far, Workbench sends
the fix list after 16:30 UTC anyway (when it is awake), flagging the run as
never stored (it started) or missing (no run by 16:30 UTC).

This conservative adapter ingests **unambiguous individual Product JSON-LD offers**.
It rejects aggregate prices, ordinary Bach/Yamaha without exceptional evidence,
non-trumpets and obvious non-Bb models; it has no warm/dark sound filter. It scores
priority makers/features and the owner's favored/disliked attributes. It interprets explicit sentiment phrases in freeform notes locally, including likes/dislikes of finishes and engineering, and style-positive but price-negative reactions. These bounded signals improve ranking without becoming hard filters. Unrecognized prose remains available to authenticated research clients; the runner does not claim arbitrary natural-language understanding or exhaustive access to unsupported sites.
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
| `GET /v1/trumpets/machine/runs?externalId=…` | `{found, runId, status, completedAt}`: whether that run was stored (after a submit timeout) |
| `POST /v1/trumpets/machine/fixlist/start` | `{externalId}`: this run has started (the fix list reads "running") |
| `POST /v1/trumpets/machine/fixlist` | Send today's fix list (once per UTC day; later calls replay `{sent: false, replayed: true}`). The runner calls it after a run started 13:00–16:00 UTC, or with `--trigger-fix-list` |

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
filters and notes-dialog/page horizontal overflow and JS errors, then drives the
inspiration board (seed and fixture enrichment, paste-to-capture, duplicate toast,
📷 upload at 390px, clipboard image paste, two-file drop, quick priority, filters,
detail autosave, click-to-load embed referrer policy, Observatory link and the
share-sheet route), captures screenshots, then deletes its schema. Inspiration
previews come from loopback fixtures and an in-memory bucket; no third party is contacted.
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
filter to revisit dismissed leads. Acquired leads remain accessible in All tracked. Leads are retried in
rotation, up to 40 per run, least recently attempted first (never-attempted, newest
first); leads on a domain that refused a check earlier in the same run are recorded
as skipped without using a slot. A lead whose page returns 404/410 is retired as
`removed` (hidden from Candidates unless you filter by that status) and not
resurrected by later searches. An
unambiguous verified offer promotes the same row, preserving first seen, feedback
and URL identity. Search hints cannot downgrade verified price/status history.
Acquired horns remain excluded from new-listing alerts.

Runs rotate 36 individual domains from the accumulated universe, with bounded slots for previously useful sources and priority for never/oldest-searched domains. Six maker/model queries rotate across the nine seed queries; four unfamiliar-maker/international exploration queries and four new-source queries run every day. At least 25% of the planned queries explore beyond the shortlist. New-source queries exclude known domains; accepted useful new domains persist automatically.
Those extra queries exclude Reverb to encourage other sources; its dedicated source
search remains enabled. Citation titles are accepted only for actual web-tool URLs.
Parsers accept individual Product JSON-LD offers, dealer product metadata with
explicit price/currency/stock, schema.org microdata with exactly one Product scope
and one price, currency and availability, and Shopify product JSON with explicit
variant stock and cart currency. Accessories sold under makers' names (cases, gig
bags, backbores, valve tops, “first aid” kits) are not instruments. Ambiguous variant prices and related products are rejected.
Shopify’s scaled prices are divided by 100 even for JPY; yen display uses whole units. Unverified leads have a lower triage threshold
(45 versus 55 for verified active offers).

Discovery is bounded to 240 page inspections and 35 minutes after starting the run;
active listing rechecks happen first, then up to 40 queued leads. Remaining promising result URLs can still enter
the queue. Source coverage records actual checks and unsupported pages, rather than
claiming exhaustive inventory. Authenticated clients can consume all freeform feedback;
the automated runner consumes structured preferences, explicit note signals and passes. Raw private notes never leave the private API in public search queries.

Deployment adds replay-safe migration `022_trumpet_candidates.sql`, API/UI changes
and the updated stdlib runner. No Caddy, Cloud Run environment or secret changes are
required. Deploy the API before the runner/UI to support `verificationState`.
The existing VM timer stays daily at 13:17 UTC; the expanded runner uses a versioned
run idempotency key; each invocation has its own ID so an incomplete run can be retried that day without replaying the old failure.
[Candidate queue screenshot](screenshots/candidates.png) uses synthetic data.


## Persistent adaptive watch (migration 023)

`trumpet_sources` stores owner-scoped domains, geography/specialty, first seen,
last searched/live/useful timestamps, query success, pages opened, verified
observations and confirmed stale-page counts. Bootstrap domains are merged with
stored sources rather than used as a whitelist. Rotation schedules 36 unique
source families/domains (eBay country aliases do not consume two slots). It keeps
at most eight useful-source slots; never/oldest searched sources fill the rest.
Discovery admits at most six findings and twelve fetched pages per domain per run
so easy marketplaces cannot flood the board. Active rechecks are exempt from these
caps and run first on every invocation, including same-day manual reruns. Sources
from legacy tracked offers are also carried into rotation. Skipped queries do not
advance source rotation or success counters. The source universe panel separates **searched domains**, **live
page domains**, and **known domains**; a query success never implies current stock.
Counts are observations, not a claim of exhaustive dealer inventory.

The profile now includes `sourceUniverse`, `watchPolicy`, and `notePreferences`.
An explicit “love the engineering / raw brass, but too expensive” note retains
those style signals and mildly penalizes same-maker offers at or above that
example's observed asking price in the same currency. “Don't like gold plate”
reduces that attribute's weight. Adjustments are bounded; acquired exclusion is
physical-horn scoped and exploration stays reserved. No note text is sent to Brave
or OpenAI search queries. Unsupported language remains private raw feedback for
other authenticated research clients.

All verified observations carry description/condition/configuration/provenance
snapshots. Changes produce `details change` events; whitespace-only description
changes are ignored. Every price movement remains in history. Alerts require a
same-currency drop of at least **3%**, or an increase of at least **15%**; currency
changes and minor adjustments do not imply comparable discounts. New sold offers
are saved as asking-price history without new-discovery alerts. When at least three
other verified, distinct same-model horns have same-currency observed asking prices,
comparisons use their median, clearly labeled as asking prices, not sale values.

Today consumes a separate `alerts` array: at most eight ranked verified physical
horns, excluding acquired, passed and unverified leads. No meaningful signal means
an empty feed. `events` and full offer histories remain available to authenticated
clients. Canonical URLs remove common eBay tracking; marketplace listing IDs are
also extracted. Serial-confirmed cross-posts share private feedback; candidate
promotion merges onto the verified horn while retaining both notes. Exact image
matches require matching model and named seller for automatic linking; weaker
matches stay explicitly unconfirmed. All tracked groups physical horns and prefers
active seller-direct offers, with alternate direct links/prices/statuses accessible
inside the card. All individual offers/history remain in Postgres.

Rechecks use live pages. Exact-product headings and explicit stock badges override
stale structured stock (related-product/footer badges are ignored). Sold/expired
badges become sold; pending/reserved/temporarily unavailable become stale until
revalidated. HTTP 404/410 becomes removed. Unsupported or blocked active pages keep
the previous facts and leave the run partial and the offer due. Freshly posted
classification requires a seller date within the last 24 hours; missing, older or
future dates are newly discovered instead. Queued candidates remain unverified
until an unambiguous source offer is available.

Deploy the API first: startup applies replay-safe `023_trumpet_watch.sql` after a
Cloud SQL backup. Preserve existing Cloud Run environment, secrets, resources and
Caddy routes. Then replace `/opt/trumpets/runner.py` and overlay the trumpet static
assets atomically on the existing site release. The daily VM timer remains **13:17
UTC**. No new secret or scheduler is required. The previous API/runner remain
compatible with the additive migration for rollback; avoid deleting source/history
rows. Run one manual combined search after rollout and check authenticated coverage,
remaining active rechecks, Today and the mobile source universe.

[Mobile source universe](screenshots/mobile-coverage.png) uses synthetic fixtures.

## Horn inspiration (migration 025)

`/trumpets/#inspiration` is a personal board for horns that inspire: YouTube
Shorts, Instagram reels, retailer pages, screenshots and photos, each with an
optional line about *why*. It is a view inside the Observatory, so it inherits
the `@jazz_private` Caddy matcher, the `/trumpets/api/*` proxy and
`trumpetPrivacy` (`private, no-store`, `noindex`, `no-referrer`) with no Caddy change.

**Capture.** Paste a link into the capture bar (a single link pasted into the
empty bar submits at once, with Undo), press Enter, paste or drop images anywhere
on the board, or tap 📷. Each image becomes its own entry; uploads run two at a
time with a local preview and Retry. A "Just added" strip offers one-tap priority
(Just inspiration / Someday / Want / Actively hunting), maker and why. Everything
except the link or image is optional; the detail dialog edits the rest with a
600 ms autosave carrying `expectedRevision` (409 → non-conflicting fields are
re-applied; the server wins on the same field).

**Storage.**
- Rows: `horn_inspirations` and `horn_inspiration_images` in `jazz_project`;
  `horn_inspiration_seeds` remembers that the Harrelson starter card was offered,
  so it is never recreated after archive or permanent delete.
- Image bytes: the existing private `GCS_BUCKET` under
  `inspiration/<user>/<inspiration>/<image>.<jpg|png|webp|gif>`, written by the API
  (raw body `POST /v1/trumpets/inspiration/{id}/images`, ≤ 10 MB, sniffed type
  must match the declared type, ≤ 12,000 px), so the bucket CORS is unchanged.
  Phones re-encode to JPEG q0.85 with a ≤ 2048 px long edge first (GIFs keep animation).
- Serving: `GET /v1/trumpets/inspiration/images/{id}` checks ownership and
  302-redirects to a 10-minute V4 signed URL (`Cache-Control: private, max-age=300`).
  No image URL is permanent and no image is committed to git.
- The `renders/` lifecycle rule does not touch `inspiration/`. Permanent delete
  removes the rows, then deletes objects best-effort; failures are logged as
  orphaned objects. `inspiration/` objects without rows can be swept manually
  (list the prefix and compare with `horn_inspiration_images.object_name`);
  automated sweeping is out of scope.

**Link identity.** `canonicalInspirationURL` (Go; mirrored in
`assets/trumpets/inspiration-model.js`, both tested against
`jazz-api/testdata/inspiration_urls.json`) builds on `canonicalTrumpetURL` and
strips share parameters (`si`, `is`, `igsh`, `mibextid`, TikTok `_r`/`_t`, …)
without changing Observatory listing identities. YouTube Shorts canonicalize to
`https://youtube.com/shorts/<id>`; `youtu.be`/`watch`/`live`/`embed` links to
`https://youtube.com/watch?v=<id>` (keeping `t`). Dedupe uses the provider media id
when present, otherwise the canonical URL: a live duplicate returns 409 ("Already on
your board"), an archived one offers Restore.

**Preview fetching (SSRF-safe).** The browser calls `POST …/{id}/enrich` after
capture (no background goroutines; the board retries pending entries older than a
minute, three at a time). Only the canonical URL leaves the server; notes never do.
The dedicated client resolves each host and refuses loopback, private, link-local
(including `169.254.169.254`), CGNAT, multicast, unspecified and reserved
addresses (IPv4 and IPv6), dials the checked IP, re-checks every redirect (max 3),
allows only http(s) on ports 80/443, sends no cookies or auth, caps HTML at 1 MB
and images at 5 MB, and uses 4 s per request / 10 s total with
`User-Agent: zachbednarke.com inspiration preview (+https://zachbednarke.com)`.
YouTube uses public oEmbed (falling back to `hqdefault.jpg`); TikTok uses its public
oEmbed; Instagram/Facebook get one Open Graph attempt and, behind a login wall,
a generic title ("Instagram reel") and the hint "Add a screenshot for a preview" —
login-page titles are never stored. Other pages use Open Graph, Twitter and JSON-LD
`Product` offers; a page price fills `price_seen` (marked **auto**) only when no price
was typed. The chosen preview image is copied into the private bucket so the board
survives link rot; no board image loads from a third-party host.

**Privacy.** Titles, descriptions and notes render with `textContent`; links pass
`safeURL()`. Filters live in memory only; nothing about inspirations is written to
localStorage, sessionStorage or query strings. YouTube and Instagram embeds are
click-to-load and are built only from the validated media id. The YouTube iframe
carries `referrerpolicy="strict-origin-when-cross-origin"` because the page's
`no-referrer` policy can make the player refuse to play (Error 153). There is no CSP
today; if one is added it needs
`frame-src https://www.youtube-nocookie.com https://www.instagram.com` and
`img-src 'self' https://storage.googleapis.com` (plus `blob:` for local upload previews).

**Observatory link and search profile.** An entry may link (never automatically) to
any `trumpet_horns` row the user owns; the detail suggests horns with the same maker
and a model prefix/contains match (the seed suggests the Harrelson MUSE reference).
Linked cards show "Tracked in Observatory · N live offers" (verified active offers) or
"Acquired ✓"; Observatory listings gain `inspirationCount` and their detail shows
"Inspiration (N)". `GET /v1/trumpets/profile` (and the machine profile) now include
`inspirations: [{maker, model, tags, priority, why}]` — up to 100 live entries, no
URLs or images — so the private search client learns what the owner is drawn to, the
same way notes are already shared with it. The local runner applies a small bounded
ranking bonus for inspired makers and tags.

**Phone share sheet.** Shared links travel in the fragment, which is never sent to
the server or written to Caddy logs, and the page rewrites it to `#inspiration`
immediately.
- iOS: create a Shortcut that *Receives URLs from the Share Sheet*, then
  **Open URL** `https://zachbednarke.com/trumpets/#inspiration/add?url=[URL-encoded Shortcut Input]`
  (optionally `&note=…`).
- Android: use a bookmark or the HTTP Shortcuts app with the same URL.
- Desktop bookmarklet:
  `javascript:location='https://zachbednarke.com/trumpets/#inspiration/add?url='+encodeURIComponent(location.href)`

API (all behind `trumpetPrivacy` + `authenticate`): `GET /v1/trumpets/inspiration?archived=0|1`,
`POST /v1/trumpets/inspiration` (idempotent `clientCaptureId`), `POST /v1/trumpets/inspiration/seed`,
`POST …/{id}/enrich[?force=1]`, `PATCH …/{id}`, `DELETE …/{id}` (permanent),
`POST …/{id}/images`, `DELETE …/{id}/images/{imageId}`, `GET …/images/{imageId}`.

[Inspiration board](screenshots/inspiration.png) · [Inspiration on a phone](screenshots/inspiration-mobile.png) ·
[Inspiration detail](screenshots/inspiration-detail.png)

## Failed-check tracking and honest freshness (migration 031)

Replay-safe `031_trumpet_check_failures.sql` adds `check_failures`,
`last_check_attempt` and `last_check_error` to `trumpet_listings` and widens the
status check to allow the server-assigned `unverifiable`. The constraint is
replaced only when it lacks that value, so replays on every jazz-api and workbench
startup are no-ops. **Deploy the API before the runner**: an older API rejects the
runner's `checkFailures` field as unknown JSON, and the runner relies on the API to
record those failures. The VM timer, its schedule and `TRUMPETS_DAILY_ENABLED`
(unset) are unchanged.

## Taste training (migration 032)

`/trumpets/#taste` turns quick reactions into a taste model that ranks listings
and steers the daily search. It is phone-first and works by touch, mouse, buttons
or keyboard.

- **Swipe:** one horn at a time. Right (or →) is "cool", left (←) "not for me",
  up (↑) "must see"; **Undo** (Z) takes the last reaction back, and every reaction
  shows a four-second toast with a one-tap **Undo**. A drag commits only when it is
  deliberate: sideways past 40% of the card width (140 px at most), or a fast fling
  (over 0.6 px/ms in the last ~80 ms, same direction, past 60 px). The drag's axis
  locks after ~10 px, the matching stamp (COOL / NOT FOR ME / MUST SEE) fades in with
  progress and is solid once releasing would commit; anything less springs back.
  Up by drag needs more than 160 px with |dy| > 2|dx|, from a mouse or pen, or by
  touch only once the card was grabbed sideways: the card is `touch-action: pan-y`,
  so vertical touch drags scroll the page (a cancelled pointer never commits) and
  touch uses the **Must see** button. Head to head has no drag gestures. The queue shows unseen horns first (active offers, newest first), then
  horns not swiped in two weeks, least certain first. Acquired and passed horns
  are left out.
- **Head to head:** two horns; tap the one you'd rather own, or **Both** (↑) /
  **Neither** (↓). Pairs are the ones whose outcome the model is least sure of
  (largest predictive sd of the difference × p(1−p)); pairs compared in the last
  two weeks are skipped, and each horn appears at most twice per batch.
- **Chips** on a card (raw brass, gold plate, patina, engraving, odd bell, vintage,
  one-off, great value, too pricey, wrong vibe) tag the physical horn. Trait chips
  add that feature to the horn; "wrong vibe" is recorded for analysis only.
- **Notes** are optional: type one, or hold **🎙 Hold to talk** (Space/Enter also
  hold) to record a voice note. Audio is stored in the private bucket under
  `trumpet-taste/<user>/…` like Commonplace audio and served by an owner-checked
  ten-minute signed redirect. No transcription. Explicit phrases in typed notes
  ("love the raw brass, but too expensive") appear as statements in the panel.
- **What I've learned** lists the strongest liked and disliked makers, model
  families and traits with weight ± sd, an overall confidence, and the features
  that moved most against a fit without the last week of reactions. **Your stated
  preferences** holds owner-editable lines ("cool horn, not dark horn"), liked and
  avoided makers and traits, and a price note.

**Storage.** `trumpet_taste_events` is append-only (an update trigger refuses
changes): swipes, pairs, tags (`add`/`remove`, latest wins), notes and undos, which
reference the event they cancel (once). A `clientId` makes each write idempotent.
Events reference offers through composite owner foreign keys; the model groups them
by physical horn. `trumpet_taste_preferences` keeps every saved version; the latest
is current. Both tables are created by replay-safe `032_trumpet_taste.sql`.

**Model** (`jazz-api/trumpets_taste.go`). Features per offer:
maker, model family, era (year or "vintage"), finish/metal, bell and leadpipe traits,
engraving, one-off/custom, provenance, price relative to the median of ≥3 distinct
same-currency verified horns (same maker first), source domain (eBay sites are one
source), plus the owner's chips. A Bradley-Terry (pairs) / logistic (swipes; "must
see" counts double, both/neither half each) model with a Gaussian prior (sd 1) is
fitted by Newton's method (at most 10 iterations, warm-started from the previous fit);
the Laplace covariance gives each weight's sd. Features seen on fewer than two
distinct horns stay at their prior (a feature unique to one horn cannot be told
apart from it), which keeps the matrix small. Existing ratings (4–5 liked, 1–2
disliked), favorites and passes count half as data. Reactions decay with a one-year
half-life. Stated likes/dislikes are prior means of ±0.5. The fit is cached in
memory per owner, keyed by the latest reaction, the preferences' revision, the
listings' model inputs and the UTC day, so board loads reuse it; "Recent changes"
compares it with a separately cached fit on reactions older than a week.

The **learned score** is `rule score + 40 × confidence × share × (2σ(affinity) − 1)`,
where confidence = n / (n + 8) over the data behind the fit (plus up to eight stated
preferences) and **share** phases the blend in over the owner's first 10 taste
reactions (swipes and head-to-head picks): with none it is exactly the rule score, so
deploying with only existing ratings changes no alert and Today's **65 threshold keeps
its meaning**. Today's alerts and the "Learned score" sort use it, and cards show
`LEARNED n` when it differs. Because ratings and favorites are already in the learned
score, the alert ranking's separate favorite (+15) and rating (±5 per star) bonuses
are scaled by `1 − share`.

**API.** Browser (gateway auth, owner-scoped, `private, no-store`; writes need
`Content-Type: application/json` and pass the same-site/Origin checks):
`GET taste` (model, weights, changes, scores, queue, pairs, tags, recent events,
preferences), `POST taste/events` (`{clientId, kind: swipe|pair|tag|note|undo, …}`;
201, or 200 on replay; 409 for a second undo), `POST taste/voice?clientId=&listingId=
[&otherListingId=][&mode=][&durationMs=]` with a raw audio body (≤ 10 MB; the audio
content type is not CORS-safelisted, so cross-site requests still preflight),
`GET taste/voice/{eventId}` (404 once the note is undone; undoing a voice note also
deletes its audio, best-effort), `GET|PUT taste/preferences` (full replacement; optional
`expectedRevision` → 409 when stale). Machine (bearer token): `GET machine/taste`
(weights with sd and counts, confidence, number of stated preferences; no listing
ids, notes or audio), `GET|PUT machine/preferences` (to seed the owner's summary),
and `GET machine/profile` now includes `taste` and `preferences`.

**Search steering** (`tools/trumpets/runner.py`, class `Taste`). Each maker, trait and
source gets an explore/exploit value `weight + 0.5 × sd` (an upper confidence bound:
liked and still-uncertain options both earn budget); stated likes add 0.5, stated
dislikes subtract 1. Something is **consistently rejected** when the owner said so,
or after ≥ 2 reactions its weight + sd is still below zero. Within the unchanged
budgets (36 domains, 240 pages, 35 minutes): up to six favored sources are scheduled
first and searched first, rejected sources only fill otherwise-empty slots, and the
per-domain page cap is 16 for favored, 6 for rejected and 12 otherwise. Maker phrases
are ranked by value (the top three appear twice in the site-query rotation; rejected
ones drop out). The six maker searches become the three best, two rotating and, when
the owner likes particular traits, one trait search ("raw brass upswept bell …").
Exploration queries, the 25% exploration share and new-source discovery are
unchanged. With no reactions or preferences the plan is identical to before. The run
notes give only counts ("Taste steering: n favored and m demoted").

**Marketplaces and blocked sites.** The owner's most valuable sources, Reverb and
eBay, refuse the VM's HTML requests (403), so each listing is checked in this order:
official API first, then the page itself, then a source-matched search-provider check.
Every result carries its provenance (`verifiedVia`: `direct`, `api` or
`search-provider`), stored per observation (`trumpet_observations.verified_via`) and
shown subtly on cards ("via official API" / "via search provider") and in the detail's
observations; direct page checks have no label.

- **Reverb (public API, no key).** `https://api.reverb.com/api/listings/<id>` with
  `Accept: application/hal+json` and `Accept-Version: 3.0` gives state (`live` →
  active, `sold`/`ordered` → sold, `ended`/`suspended` → removed, anything else stays
  unverified), price and currency (refetched with `X-Display-Currency` set to the
  listing's own currency, so a converted display price is never stored), US shipping
  in the same currency, published date, photos, shop and location. Ids come from any
  `reverb.com/item/<id>-slug` link, including `applink.reverb.com`, `m.reverb.com` and
  links wrapped in a query parameter. Discovery runs every day: newest first in the
  Trumpets category (`category_uuid` `d1c7768e-…`), price ≥ 800, one search per term
  from the taste model's plan (its three best makers, two rotating makers, liked
  traits, and one search for everything newest). Discovery runs right after the
  rechecks and **before** the general web search, with its own budget of 40 API
  requests (searches plus currency refetches): behind the web search it shared the
  240-page and 35-minute budgets and could be skipped entirely. Its coverage note
  counts results and why each was not kept (already tracked, low score, not a
  verifiable Bb trumpet offer). US shipping is Reverb's `US_CON` (contiguous US)
  rate. API 404/410 → removed. Rate limits
  and 5xx get at most three attempts with backoff (`Retry-After` honoured, ≤ 30 s).
- **eBay (search provider, no developer key).** eBay pages always refuse the VM, and
  there are no eBay developer keys, so OpenAI web search is the primary path: every
  run sends the same terms as `site:ebay.com … Bb trumpet` queries through the
  Responses `web_search` tool (eBay results from the general plan join them) and
  screens each `ebay.com/itm/<12-digit id>` result with the check below. A new eBay
  item found this way becomes an unverified **lead** (Candidates), never a
  verified-active offer: no price, stock or date is asserted. Tracked verified eBay
  offers skip the doomed page fetch and go straight to that check; eBay leads wait
  for review without using the daily lead-check slots (a run note counts them).
  Only eBay's own domains (ebay.com, .co.uk, .de, .fr, .it, .es, .ca, .com.au, .ie,
  .at, .ch, .nl, .be, .pl and their subdomains) and reverb.com count; lookalike hosts
  never match.
  *Optional:* the runner also contains a Browse API client (application token from
  `EBAY_CLIENT_ID`/`EBAY_CLIENT_SECRET`, cached in memory until shortly before expiry
  and never logged; `item_summary/search` in category 16214 "Trumpets", newly
  listed, fixed-price/auction/best-offer, USD price filter; `get_item_by_legacy_id` for
  checks). It is used only if those variables are ever set; nothing else changes.
- **Search-provider check** (`verify_via_search`). When a page answers 401/403/429 or
  a bot challenge (and for eBay, as above), one Responses call with `web_search`
  (`search_context_size: medium`, `include: web_search_call.action.sources`) asks for
  that exact URL's state (available, sold, ended, unknown), title, price, currency and
  shipping as strict JSON (structured output schema). The answer is accepted **only**
  when a `search` action's results include the same listing (same canonical URL, or
  the same eBay / Reverb item id), the answer carries a `url_citation` of that same
  listing, **and** the JSON matches the schema. The model opening the given URL itself
  (`open_page` / `find_in_page`) is not evidence. Otherwise the check is recorded as
  failed, as before; generated prose is never evidence. "Available" confirms an
  already verified offer (and a stated price with its currency). A **single** "sold"
  or "ended" never retires an offer: it is recorded as a failed check ("search
  provider reports ended (unconfirmed)"), and only the same report on a later UTC day
  marks it sold/removed. Model: `TRUMPETS_VERIFY_MODEL`
  (default: `TRUMPETS_SEARCH_MODEL`, itself `gpt-4.1-mini`). At most
  `TRUMPETS_VERIFY_LIMIT` calls per run (default 60), spent on active, high-score
  tracked offers. New eBay results have their own `TRUMPETS_EBAY_VERIFY_LIMIT`
  (default 12), so rechecking tracked eBay offers can no longer use up discovery.
  The general web search stops at 30 minutes, leaving eBay discovery time before
  the 35-minute budget. Logs and run notes hold counts only; the run log's
  `marketplace APIs` line carries both coverage notes.
- **Coverage.** "Reverb API" and "eBay via web search" (or "eBay Browse API") appear in
  source coverage with their searches, records inspected and verified offers, under
  the `reverb.com` and `ebay.com` domains. A failed Reverb/eBay search makes the run
  partial like any search-provider failure; eBay discovery without `OPENAI_API_KEY`
  is recorded as skipped.

If the API predates the new `verifiedVia` report field (it answers 400 "invalid run
JSON" / unknown field), the runner resubmits the run once without it, so deploying
the runner first only loses the provenance labels.

Tests use a fake Responses endpoint and fictional recorded fixtures in
`tools/trumpets/fixtures/` (a source match and a mismatch; Reverb and eBay shapes).

**Runner environment** (`/etc/trumpets/runner.env`, root-owned, mode 0600; unchanged
path): `TRUMPETS_API_URL`, `TRUMPETS_MACHINE_TOKEN`, `OPENAI_API_KEY` (already there)
and/or `BRAVE_SEARCH_API_KEY`. Optional: `TRUMPETS_SEARCH_MODEL`,
`TRUMPETS_VERIFY_MODEL`, `TRUMPETS_VERIFY_LIMIT`, `TRUMPETS_EBAY_VERIFY_LIMIT`. Reverb needs no key. Only if eBay
developer keys are ever created: `EBAY_CLIENT_ID`, `EBAY_CLIENT_SECRET` (an eBay
developer *production* keyset; the application token uses the public
`https://api.ebay.com/oauth/api_scope`) and optionally `EBAY_TRUMPET_CATEGORY_ID`
(default 16214).

**Accessories.** The accessory filter now rejects only items that *are* accessories
(no horn word left once accessory phrases are removed, or an accessory is the head
noun before "with", ",", "+", "includes", "for"…). "Bb trumpet, mod kit, 5mm
mouthpiece" and "trumpet with extra leadpipe" are horns; "trumpet mouthpiece",
"leadpipe for trumpet" and "piccolo trumpet" are not.

**Deploy.** API first: the deploy workflow's Cloud SQL backup, then startup applies
`032_trumpet_taste.sql` (two new tables, indexes, an update trigger, and the
`verified_via` column on `trumpet_observations`, defaulting existing rows to
`direct`). No new Cloud Run environment variables, secrets or Caddy routes:
everything sits under the existing `/trumpets/api/*` proxy and private bucket.
**Deploy the API before the runner** (the new runner sends `verifiedVia`; against an
older API it resubmits without it, losing only provenance). An older runner keeps
working against the new API.
Then reinstall the runner on the VM (`/opt/trumpets/runner.py`; the service, timer
and env file path are unchanged; it now also reaches `api.reverb.com`, and
`api.openai.com` as before). Optionally seed preferences with
`PUT /v1/trumpets/machine/preferences`.

[Taste on desktop](screenshots/taste-desktop.png) ·
[Swipe on a phone](screenshots/taste-swipe-mobile.png) ·
[Head to head on a phone](screenshots/taste-pair-mobile.png) (synthetic fixtures)

## Fast ingest and a runner that survives slow responses (migration 033)

The first taste-training run was stored, but the runner crashed: `POST
/machine/runs` took 26.6 s and the runner's 25 s read timeout fired first.

- **Ingest.** Upserting ~150 listings made one database round trip per statement,
  ~1,700 in all (~15 ms each from Cloud Run to Cloud SQL). The run's writes are
  now pipelined (`trumpets_pipe.go`): writes whose results are not needed are queued
  and sent with the next read in the same transaction, so every read still sees
  every earlier write. The owner's listings and latest snapshots are prefetched
  once, so rechecking a tracked offer needs no lookup; a listing this run already
  wrote is looked up live. Failed checks are one statement. A run of 150 listings
  and 150 failures over a 300-listing board takes ~46 round trips instead of ~1,700
  (`TestTrumpetIngestRoundTrips` bounds it at 60 and at 3 s with a simulated 5 ms
  round trip; it was 10.3 s). The API has Cloud Run concurrency 1, so this also
  stops runs from blocking board loads.
- **Board and taste reads.** JSON responses are gzip-compressed for clients that
  accept it; `priceHistory` sends an observation's snapshot only when it differs
  from the previous one (`null` otherwise; the detail lists each version once);
  `trumpet_events(listing_id, occurred_at)` is indexed for the per-listing
  history; the taste endpoints skip loading the histories they never use.
- **Runner.** The submit gets 180 s. If the response still never arrives (or the
  API answers 429, 502, 503 or 504), the runner
  asks `GET /machine/runs?externalId=` (after 15, 30 and 60 s) whether the run was
  stored, and submits again only if not. That is safe: a run is stored once per
  `externalId` under the owner's lock, and a repeat replays it. Against an API
  without the lookup it relies on that replay. Any failure exits non-zero with one
  JSON line on stderr (`{"error": "trumpet runner failed: …"}`), never a traceback.

**Deploy.** API first (migration 033 adds one index, `IF NOT EXISTS`). Then copy
`tools/trumpets/runner.py` to `/opt/trumpets/runner.py` on the VM; the service,
timer and env file are unchanged. The new runner against an older API still works
(the lookup answers 404, so it falls back to the idempotent resubmit).

## Listing photos (migration 034)

Many Taste cards showed the placeholder drawing although the source had photos:
Reverb listings that were no longer due were never rechecked, imported and lead
records were stored without photos, multi-valued or `http://` `og:image` tags and
protocol-relative Shopify JSON images were dropped, and a recheck without photos
wiped the ones a listing had.

- **Runner.** Every path that reaches a listing keeps its photos, up to 6 absolute
  HTTPS URLs at a bounded large size where the CDN's URL sets it (Shopify `_1600x`,
  Squarespace `?format=1500w`, eBay `s-l1600`; other URLs as the page gives them;
  sizes of one photo, e.g. Weebly `_w640`/`_w2560`, counted once), never logos,
  placeholders, icons, banners, `data:` URIs or tracking pixels:
  Reverb's public API (`photos[]._links.full`/`large_crop`, else `_links.photo`) on
  discovery and recheck; a page's schema.org Product `image` (a product described
  twice counts once; a page with several products gives none), the first main
  product gallery only (WooCommerce `data-large_image`, Squarespace `data-image`,
  Shopify media; never related, upsell or cross-sell sections or `ul.products`
  loops, whose photos are other horns),
  `og:image`/`twitter:image` and microdata; Shopify's `/products/<handle>.js`;
  eBay only through the Browse API when keys exist. Lead hints carry the photos of
  the page they came from. A tracked listing without photos whose page was reached
  but did not verify an offer gets them through the run's `imageUpdates`.
- **eBay without keys.** Item pages refuse automated requests (403), the
  Responses `web_search` sources carry only URLs and titles (no image), and
  `i.ebayimg.com` answers 200 with a stock image even for made-up ids, so a
  model-reported image URL could not be checked against the item. eBay listings
  stay without photos unless `EBAY_CLIENT_ID`/`EBAY_CLIENT_SECRET` are set.
- **API.** A run's listing upsert keeps the stored photos when it brings none, and
  replaces them when it brings new ones. `imageUpdates: [{id, images}]` (any run
  kind, at most 1000, 1–20 HTTPS URLs each) only fill a listing that has no photos;
  the response reports `imagesAdded`. Runs of the new kind `images` carry image
  updates only: no listings, checks or coverage, no observation or `last_checked`
  change, replayed per `externalId` like every run, and left out of the board's
  latest-run and last-success freshness.
- **Taste.** The swipe queue lets one photo-less card through in every five while
  cards with photos remain, then shows the rest. A photo-less card (or one whose
  photo fails to load) shows **View listing ↗**, opening the source in a new tab;
  pressing it is never a swipe or a head-to-head choice (in head to head it sits
  beside the choice button, not inside it). Swipes and pair choices send
  `meta: {"hadPhoto": true|false}` (pairs: both sides had a photo), stored in the
  new `trumpet_taste_events.meta` column (`{}` for earlier events, which keep full
  weight). The model gives a reaction made without a photo half weight.

**Deploy.** API first (migration 034 widens the `trumpet_runs.kind` check to allow
`images` and adds `trumpet_taste_events.meta`; both replay-safe). The new taste page
sends `meta`, which an older API rejects, so ship the static site with or after the
API. Then copy `tools/trumpets/runner.py` to `/opt/trumpets/runner.py` on the VM
(service, timer and env file unchanged; against an older API it resubmits a daily
run without `imageUpdates`). Run the one-time backfill with the same environment:

```sh
sudo systemd-run --wait --pipe --property=EnvironmentFile=/etc/trumpets/runner.env \
  --property=DynamicUser=yes /usr/bin/python3 /opt/trumpets/runner.py --backfill-images
```

It walks every tracked listing without photos (Reverb API, then the eBay Browse
API with keys, then the page and a Shopify product's JSON). After a refusal (401,
403, 429 or a bot challenge) it makes no further requests to that source for the
rest of the run: the Reverb API, the eBay API, or that one site. It stays within
caps of 450 listings, 200 Reverb calls, 200 page fetches
(30 per domain, a second apart), 60 eBay calls and 25 minutes. It prints counts only
and submits one `images` run (`image-backfill-<time>`); rerunning it later only
tries listings that still have no photos.

## Dealer pages that verify, LLM page reading and the discovery page budget (migration 036)

Daily rechecks failed on several dealer sites with "page did not expose a single
verifiable offer", hornstash.com timed out, jamiejazz.com raised `ValueError`, and
discovery always used its 240 pages. Causes, found on live pages:

- **Pages without a usable Product JSON-LD.** WooCommerce shops built with page
  builders (horntrader.com) render price and stock in widgets only; Rain POS
  shops (jlandressbrass.com) expose a GA4 `view_item` event and a "This item is in
  stock." line; Lightspeed shops (hornstash.com) a grouped microdata price without
  currency or stock; classifieds (kleinanzeigen.de, bazos.cz) and hand-built
  dealer pages (jamiejazz.com) their own layouts.
- **One product described twice.** Shopify themes plus SEO apps (tcgakki.com) emit
  two Product nodes for the same item (one inside `@graph`); the runner treated
  any second product as "related products" and gave up.
- **Retitled listings.** A tracked offer matched only on its exact stored title, so
  "【値下げ】" (price cut), "Back in stock!" or a sold prefix made every recheck fail.
- **Maker attribution.** A maker merely named in the description ("plays like a
  Bach") outranked the structured brand, and the Bach/Yamaha rule then dropped
  the horn (austincustombrass.biz, hornguys.com, store.ishibashi.co.jp).
- **hornstash.com.** Stored URLs drop `www.` (`canonical_url`), and the bare
  hostname is a registrar URL forwarder that does not serve HTTPS: the connection
  hangs or is reset. The site itself (www) answers in under a second.
- **jamiejazz.com.** `https://jamiejazz.com/…` answers 301 to
  `http://www.jamiejazz.com/…`; the redirect guard refused the `http` target and
  raised `ValueError("Only public HTTPS URLs allowed")`.

What changed (all in `tools/trumpets/runner.py`):

- **Fetches** (`fetch_page`): a per-site timeout (`SITE_TIMEOUTS`), up to three
  tries with backoff on timeouts, resets and 502–504 (`Retry-After` up to 30 s; 403,
  404 and 429 are not retried), and when a bare host gives no response at all the
  same URL on `www.`, remembered for the run. A host that never answers gets one
  try per page for the rest of the run. Only a successful `www.` answer is used:
  a `www.` HTTP error never stands in for the bare host's own error (so a
  listing is never marked removed because of the `www.` host). A same-site redirect to `http://` is
  followed over HTTPS; any other `http` redirect is still refused.
- **Generic extractors, first:** JSON-LD in more shapes (`@graph`/`mainEntity`,
  offer lists normalized before comparing, `AggregateOffer` only with a single
  price, `priceSpecification`, string prices with grouping or comma decimals,
  zero meaning "no price", lenient JSON for raw newlines/trailing commas, the
  page's own `product:price:currency` when the offer omits it); the same product
  described twice counts once only when every copy names this page and their
  facts agree; OpenGraph `og:availability`/`og:price:*` and grouped amounts; the
  page's analytics `view_item` event (one item, named like the page heading) with
  an explicit stock statement in the buy box (hidden elements, navigation and
  related sections are ignored; "not in stock", "nicht auf Lager", "niet op
  voorraad", "ej i lager" and "in stock soon"/pre-order/on order are read as not
  in stock). Then the shop platform's own endpoint for this
  URL: Shopify `.js` + `cart.js`, the WooCommerce Store API
  (`/wp-json/wc/store/v1/products?slug=`, permalink must match, minor units,
  `is_in_stock`; variable products with differing prices are ambiguous) and
  Lightspeed `?format=json` (product URL must match, shop currency).
- **Site layouts** only where needed: Kleinanzeigen (`#viewad-main-info` offer
  scope, reserved/deleted labels), Bazoš ("Cena:" row with Kč/€/zł), Jamie's
  Trumpet Gallery (heading price plus Inquire, or SOLD!; "$" is USD only because
  the page's store data gives a US address). Classified ads with a symbolic price ("1 Kč", "VB 0") are
  not verified.
- **Strictness kept.** Several different products on a page still give none;
  related-product prices are never read (no extractor looks inside related or
  product-loop sections, and platform endpoints are keyed by this URL's slug);
  currency is never taken from a domain. A retitled tracked offer matches only
  when the page says it is the listing's URL (canonical, og:url or product url),
  or comes from a URL-keyed platform endpoint or site layout, with the same maker
  and no conflicting listing id; the maker must be the one the page itself names
  (not the tracked listing's) and every model word of the tracked title must be
  in the new title, so a reused URL ("Phat Boy" where a "Chicago" was) or a
  generic title ("Used professional Bb trumpet") is never matched; the stored
  title follows an accepted retitle. A listing URL that now redirects to another
  page verifies nothing. Japanese and European accessory/other-instrument
  words (マウスピース, ケース, ピッコロ, C管, mondstuk, Trompetenmundstück…) are
  rejected; "Trompete" and "trubka" count as horn words. A shop "brand" that is
  only a category ("Bb-trumpet") is ignored; a recheck may fall back to the
  tracked listing's own maker, a new find never is.
- **LLM page reading** (`PageLLM`), last: only for a page the runner fetched
  itself (HTTP 200, and not redirected away from the listing's page) that no
  extractor could read, only for rechecks, and never on a page the extractors
  found ambiguous (several products, disagreeing copies) or excluded (accessory,
  other instrument, maker rule). The page is cleaned (no scripts, styles,
  navigation, header, footer, forms, hidden, screen-reader-only, struck-through
  `<del>`/`<s>` or related-product sections; title, main text and JSON-LD kept;
  about 12,000 characters) and sent as untrusted data inside a random
  per-request delimiter to the Responses API (`TRUMPETS_VERIFY_MODEL`, else
  `TRUMPETS_SEARCH_MODEL`; strict JSON schema; no tools). A reading is accepted
  only when all of these hold:
  - the model's title is on the page, and it says this is a single Bb trumpet
    product page with a known availability ("unknown" never verifies and never
    marks anything sold);
  - the quoted price text is a money amount in the cleaned page text, with digit
    and separator boundaries on both sides ("4.990" is not inside "14.990 EUR"),
    and parses to the stated price;
  - the price is at least 100 USD-equivalent, is not in a sentence about
    shipping, deposits, monthly payments, "from", "save", "was" or old/list
    prices, and is the page's main price: its only money amount, or the one
    nearest the main heading;
  - the currency is the amount's own ISO code or unambiguous symbol; for "$",
    "¥" or "kr" it is the single currency the page declares for its product (its
    OpenGraph/product meta or microdata, the JSON-LD offer, or its own
    `view_item` event), never a code mentioned elsewhere ("we accept CAD");
  - the page says it is this listing's URL (canonical, og:url or product url);
    a page that names no URL does not bind a reading, for leads either;
  - for a tracked offer, the price moved at most 30% and kept its currency;
    otherwise the check fails with "price change needs confirmation".
  An accepted reading keeps the tracked listing's score, tags and rationale.
  Budget: `TRUMPETS_PAGE_LLM_LIMIT` pages per run (default 80; 0 turns it off;
  a non-number logs a warning and uses 80) and 8 minutes including retries,
  spent in recheck order (active, high-score offers first). It logs counts only.
  Accepted offers carry `verifiedVia: "page-llm"`.
- **Discovery page budget.** Pages were spent on results another query had
  already returned (only kept results were remembered), on images/PDFs,
  category/search/tag/blog pages, further result pages, cart/account pages,
  accessories, other instruments, and on domains that had already refused the
  run. `discovery_skip` drops those before any fetch, a URL is fetched at most once
  per run, and refused domains are not fetched again. Sources that verified the
  most offers are planned first. The cap stays 240 (`DISCOVERY_PAGES`): the run
  note now records what the fetched pages gave and what was not fetched and why,
  so a later change to the cap can be based on that data.

**Deploy.** API first: migration `036_trumpet_page_llm.sql` widens the
`trumpet_observations.verified_via` check to allow `page-llm` (replay-safe: only
while it does not yet allow it), and the API accepts it. Against an older API the
runner resubmits such offers as `direct` (the closest label; the evidence still
says the page was read by a model). An older runner works unchanged against the
new API. Then copy `tools/trumpets/runner.py` to `/opt/trumpets/runner.py` on the
VM; the service, timer and env file path are unchanged, and the runner uses the
existing `OPENAI_API_KEY`. Optional: `TRUMPETS_PAGE_LLM_LIMIT` in
`/etc/trumpets/runner.env`. The VM also needs outbound HTTPS to the dealers' own
`/wp-json/wc/store/…`, `?format=json` and `www.` hosts, which are the same sites
it already fetches.
