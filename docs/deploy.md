# Deployment

Merging to `main` deploys automatically. `.github/workflows/deploy.yml` runs after
the "Trumpet and Jazz tests" workflow passes on a push to `main`:

1. Optional Cloud SQL backup (set the repository variable `JAZZ_CLOUD_SQL_INSTANCE`).
2. `gcloud run deploy jazz-api --source jazz-api` in `parabolio-prod`. Startup
   applies the replay-safe migrations. Existing environment, secrets and settings
   are kept. The API's `/health` must answer before the site is published.
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
   removes the directory however the publish ends. The hash read is capped at
   120 s (falling back to uploading every file). The log prints each
   part's time and, per attempt, how long the VM took to receive the bytes
   (`received N bytes in M ms`), which tells a slow transfer from a session
   that will not close. Films in
   `/srv/zachbednarke.com/films` are separate and untouched. The log prints the
   previous release and a one-line rollback.

Run it by hand from the Actions tab (Deploy, Run workflow) to redeploy `main`.

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
