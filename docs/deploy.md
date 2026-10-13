# Deployment

Merging to `main` deploys automatically. `.github/workflows/deploy.yml` runs after
the "Trumpet and Jazz tests" workflow passes on a push to `main`:

0. A guard that stops the deploy before anything changes if production holds
   something this commit does not (see [Out-of-band deploys](#out-of-band-deploys)).
1. Optional Cloud SQL backup (set the repository variable `JAZZ_CLOUD_SQL_INSTANCE`).
2. `gcloud run deploy jazz-api --source jazz-api` in `parabolio-prod`. Startup
   applies the replay-safe migrations. Existing environment, secrets and settings
   are kept. The revision is labelled `commit-sha=<commit>`. The API's
   `/health` must answer before the site is published.
3. A new static release on `actual-server` (`actual-budget-zb`, `us-west1-a`, over
   IAP) made by `deploy/publish-static-release.sh`: it copies the live release,
   overlays `index.html`, `.nojekyll`, `assets/`, `jazz/`, `trumpets/`, `commonplace/` and `privacy/` from the
   commit, then switches `/srv/zachbednarke.com/current` atomically. Only the
   files whose bytes differ from the live release are uploaded
   (`deploy/package-static-release.py` compares SHA-256 hashes read from the VM;
   if they cannot be read, every file is); the overlay makes the release the
   same either way. `deploy/upload-static-release.sh` sends that archive and
   the publish script as one tar on an ssh session's stdin (not
   `gcloud compute scp`, which through the IAP tunnel intermittently sat about
   240 s before exiting, even for 32 KB). On the VM the tar's SHA-256 must
   equal the runner's before it is unpacked into `/tmp/site-release-<sha>`
   (refused if it is a symlink or not the ssh user's; made 700). Each attempt
   is capped at 120 s plus 1 s per 4 KB (at most 900 s, so a full-archive
   fallback at a few KB/s still fits) and retried once; a session that hangs
   only after the VM confirmed the hash counts as done. If no attempt
   verifies, the remote directory is removed and nothing is published. Before
   anything runs with sudo, the publish call re-checks that the directory is
   still the ssh user's and not a symlink, and that the archive and the
   publish script there have the runner's SHA-256s; it is capped at 300 s and
   removes the directory however the publish ends. The live hashes come from
   the guard's read (`deploy/read-live-release.sh`). Live files that the
   commit does not publish are listed as a warning ("Live files not in the
   repository") and kept; nothing is deleted. The log prints each
   part's time and, per attempt, how long the VM took to receive the bytes
   (`received N bytes in M ms`), which tells a slow transfer from a session
   that will not close. Films in
   `/srv/zachbednarke.com/films` are separate and untouched. The log prints the
   previous release and a one-line rollback.

Run it by hand from the Actions tab (Deploy, Run workflow) to redeploy `main`.

## Out-of-band deploys

In early October 2026 production was deployed from outside this repository (the
static site on the VM and the `jazz-api` Cloud Run service), and the next
automatic deploy of `main` silently replaced it. The first step of the
workflow now refuses to deploy over anything it cannot account for. It needs
the full history (`fetch-depth: 0`) to ask whether one commit is an ancestor
of another (`deploy/check-deployed-commit.sh`).

- **Static site.** One ssh call (`deploy/read-live-release.sh`, two tries of
  at most 120 s) reads the live release's `.release-commit`, which
  `publish-static-release.sh` writes, together with the live hashes and file
  list. The deploy stops unless that commit is the one being deployed or an
  ancestor of it. A commit this repository does not have, one that is not an
  ancestor (including deploying an older commit over a newer one), a missing
  or unreadable `.release-commit`, and a failed read all stop it.
- **Cloud Run (`jazz-api`, and `workbench` with `WORKBENCH_ENABLED`).** Each
  deploy labels the service and its revision `commit-sha=<commit>`
  (`--update-labels`). Before deploying, `deploy/check-cloud-run-commit.sh`
  reads every revision serving traffic: a `commit-sha` that is not an ancestor
  (or is unknown) stops the deploy. A revision with no label only warns: the
  revisions serving when the label was added were all deployed by this
  workflow from `main`, and the next deploy labels them. Because a
  `gcloud run deploy` from anywhere else keeps the service's template, label
  included, a serving revision whose `serving.knative.dev/creator` is not the
  deploy service account also stops the deploy, unless its image is one a
  revision by the deploy service account runs (a settings change made by hand,
  such as a new secret, on the workflow's code).

When it stops, the log says what is live and why. Merge whatever production has
into `main` (the merge then deploys normally). To throw it away instead, run
Actions, Deploy, Run workflow with **allow_overwrite_out_of_band** checked: the
same checks run but only warn, and the deploy overwrites production.

Not covered: a release that kept a workflow `.release-commit` but had files
edited in place on the VM. New files there are listed by the orphan warning;
changed ones are not detected.

New private apps can need a one-time Caddy change on the site VM that the
workflow does not make. Commonplace needs its API route once:
`sudo python3 deploy/install-commonplace-route.py` (see
[docs/commonplace/README.md](commonplace/README.md#deploy)).

Workbench runs as a second Cloud Run service, `workbench`, from the API's
image. It is created once by hand and needs its Caddy route once (see
[docs/workbench/README.md](workbench/README.md#deploy)); after that, with the
repository variable `WORKBENCH_ENABLED=true`, each deploy ships the new image
to it as well.

## One-time setup

The workflow does nothing until the repository variable `DEPLOY_ENABLED` is
`true`. It authenticates with Workload Identity Federation, so no key is stored
in GitHub. Open [Google Cloud Shell](https://shell.cloud.google.com) (already
signed in as you) and run:

```sh
git clone https://github.com/zbednarke/personal-site.git && cd personal-site
bash deploy/setup-deploy-auth.sh
```

It creates the `github-deploy` service account with only the roles a deploy
needs, trusts only this repository's `main` branch, checks whether the site VM
uses OS Login (and picks the matching SSH role) and confirms that
`/srv/zachbednarke.com/current` is a symlink. It is safe to re-run. It ends by
printing the repository variables to add under Settings, Secrets and variables,
Actions, Variables. Then run Actions, Deploy, Run workflow.
