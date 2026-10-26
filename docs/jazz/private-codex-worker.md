# Magic Film with a ChatGPT subscription

The website queues jobs in Postgres. A dedicated, private GCE worker runs the
official Codex CLI using the owner's ChatGPT subscription and renders validated
edit plans with FFmpeg. It does not expose an HTTP service or use the public
repository's CI to run subscription sessions.

## Deployment

- VM/network/service account: `jazz-film-private`, project `parabolio-prod`, zone
  `us-central1-a`. The network permits SSH only through IAP. Its external address
  supplies outbound internet access; no public web ports are opened.
- Debian 13, e2-medium, 40 GB persistent disk. The VM, disk and storage have normal
  GCP costs even though model work uses subscription limits.
- Official `@openai/codex` version **0.164.0** is pinned. Update deliberately and
  rerun the structured-output and login tests when upgrading.
- `magic-film` runs the Go service; a separate `magic-codex` OS user runs Codex.
  The latter cannot read the worker's DB environment file or obtain a GCP token
  from metadata. DNS queries to GCE's resolver are allowed. No API keys are
  passed to Codex. Only generated candidate metadata and images are supplied.
- DB connection uses the authenticated Cloud SQL Go connector. The service
  account has Cloud SQL client, logging writer, recording-bucket object access,
  and permission to sign its own short-lived media URLs.

Provision and install from a trusted administrator workspace:

```sh
python deploy/provision-private-film-worker.py          # inspect commands
python deploy/provision-private-film-worker.py --apply
(cd jazz-api && go test ./... && go build -trimpath -o /tmp/private-film-jazz-api .)
python deploy/install-private-film-worker.py --binary /tmp/private-film-jazz-api
```

Both scripts accept `--project` and `--zone`; set `GCLOUD` to a CLI path if needed.
The installer reads only the existing API's database/bucket configuration,
transfers it through IAP without printing it, and leaves the worker stopped.
It never transfers subscription credentials. Keep `/etc/magic-film/worker.env`
root-owned and mode 600. Reinstall configuration after rotating DB credentials.

Deploy the application through the repository's normal reviewed release flow,
including the pre-migration Cloud SQL backup. Migration 045 adds provider and
retry fields and paused statuses. Old jobs remain `provider=api`; no historical
failed job is automatically resubmitted or switched to another billing mode.

Set the repository Actions variable `MAGIC_FILM_PROVIDER=codex` for the reviewed
release. The normal Deploy workflow applies it with the release's deployment
identity and provenance. If the variable is unset, deployments preserve the
existing service setting; they never default back to API billing. This variable
is not a credential and gives CI no access to the subscription login.

After the new API/schema is deployed and subscription authentication is tested:

```sh
gcloud compute ssh jazz-film-private --project=parabolio-prod --zone=us-central1-a \
  --tunnel-through-iap --command='sudo systemctl enable --now magic-film-worker'
```

New requests now remain in the private queue instead of launching the API-backed
Cloud Run Job. Normal releases preserve that environment setting. The private
worker binary is upgraded explicitly with the installer, then restarted; CI has
no login file and no permission added here to access it. The legacy Cloud Run
job can remain available for historical operations, but is never a fallback.

## Sign in and preserve the session

```sh
gcloud compute ssh jazz-film-private --project=parabolio-prod --zone=us-central1-a \
  --tunnel-through-iap \
  --command='sudo -u magic-codex /usr/local/libexec/magic-codex-session login --device-auth'
```

Complete the official OpenAI device flow. Check with the same command ending in
`login status`. It must report **Logged in using ChatGPT**. API-key logins are
rejected by the worker. Codex execution also enforces `forced_login_method=chatgpt`
and ignores user/project configuration overrides for model routing.

Codex owns `/var/lib/magic-codex/.codex`, mode 700, on persistent disk. It refreshes
its own credentials in place. Do not bootstrap over the refreshed file on every
run, clone it to concurrent workers, put it in GitHub secrets, or attach it to an
issue. Reauthentication is occasionally necessary after revocation or expiry.
Disk recreation requires a new login; ordinary service/VM restarts do not.

## Pauses, resume and cancellation

- A Postgres advisory lock admits one worker for the entire credential stream.
  Losing the lock connection stops work. systemd restarts the service.
- The frozen selection is stored in Postgres. Prepared energy summaries and
  representative images survive pauses in `/var/lib/magic-film/jobs/<job-id>`.
  Validated edit plans are stored in Postgres before rendering. A saved plan is
  reused after restart without another model request.
- Subscription usage/rate limits produce `waiting_usage`. The UI explains the
  pause and displays the **next check**, not an invented reset time. The worker
  retries after 30 minutes; a further limit extends that pause.
- Missing/revoked authentication produces `sign_in_required`. Every two minutes
  the worker checks whether subscription authentication is available again.
- Paused jobs retain the one-active-job constraint and can be cancelled. Status
  refresh never creates another job. There is no paid API fallback.
- Work interrupted by service shutdown is requeued. All original validation
  still runs before rendering: candidate bounds, non-overlap, user-curated
  selections, and output duration/media checks.

Subscription work uses the account's default Codex model. Set
`MAGIC_FILM_CODEX_MODEL` on the API only after verifying the requested model is
available to that account. This is independent of the legacy API model setting.

## Verification and operations

Run `go test ./...` with the repository's disposable Postgres test variables,
the existing JS suite, and `node tools/jazz/browser-check.cjs`. Codex unit tests
use a fake launcher, verify env isolation, schema/image arguments, quota/auth
classification, and stale-output rejection. Database tests verify persisted
pause state, preserved checkpoints, active-job exclusion, and the worker lock.

After signing in, perform a small synthetic structured-output/image test on the
private worker, then verify a real film through the private UI. Do not send raw
auth files or private media to CI. Inspect service health with `systemctl status
magic-film-worker` and `journalctl -u magic-film-worker`. Use the UI job reference
to correlate failures. Worker logs omit model transcripts and auth tokens.

To stop processing without switching to paid API usage, stop the private worker
and leave `MAGIC_FILM_PROVIDER=codex`; queued jobs stay queued. Roll back the
worker binary separately if necessary. Changing the provider to `api` is an
explicit billing change, never an automatic recovery action.

Official references:
- https://developers.openai.com/codex/noninteractive/
- https://developers.openai.com/codex/auth/ci-cd-auth
