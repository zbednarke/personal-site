# Deployment

Merging to `main` deploys automatically. `.github/workflows/deploy.yml` runs after
the "Trumpet and Jazz tests" workflow passes on a push to `main`:

1. Optional Cloud SQL backup (set the repository variable `JAZZ_CLOUD_SQL_INSTANCE`).
2. `gcloud run deploy jazz-api --source jazz-api` in `parabolio-prod`. Startup
   applies the replay-safe migrations. Existing environment, secrets and settings
   are kept. The API's `/healthz` must answer before the site is published.
3. A new static release on `actual-server` (`actual-budget-zb`, `us-west1-a`, over
   IAP) made by `deploy/publish-static-release.sh`: it copies the live release,
   overlays `index.html`, `.nojekyll`, `assets/`, `jazz/` and `trumpets/` from the
   commit, then switches `/srv/zachbednarke.com/current` atomically. Films in
   `/srv/zachbednarke.com/films` are separate and untouched. The log prints the
   previous release and a one-line rollback.

Run it by hand from the Actions tab (Deploy, Run workflow) to redeploy `main`.

## One-time setup

The workflow does nothing until the repository variable `DEPLOY_ENABLED` is
`true`. It authenticates with Workload Identity Federation, so no key is stored
in GitHub.

```sh
RUN=parabolio-prod
SITE=actual-budget-zb
REPO=zbednarke/personal-site
NUMBER=$(gcloud projects describe $RUN --format='value(projectNumber)')
SA=github-deploy@$RUN.iam.gserviceaccount.com

gcloud iam service-accounts create github-deploy --project $RUN --display-name "GitHub deploy"

# Cloud Run source deploys (and optional pre-deploy backups).
for role in run.admin iam.serviceAccountUser cloudbuild.builds.editor artifactregistry.writer storage.admin serviceusage.serviceUsageConsumer cloudsql.editor; do
  gcloud projects add-iam-policy-binding $RUN --member "serviceAccount:$SA" --role "roles/$role" --condition None
done
# SSH to the site VM through IAP with sudo (OS Login).
for role in iap.tunnelResourceAccessor compute.osAdminLogin compute.viewer; do
  gcloud projects add-iam-policy-binding $SITE --member "serviceAccount:$SA" --role "roles/$role" --condition None
done

# Let only this repository's main branch act as the service account.
gcloud iam workload-identity-pools create github --project $RUN --location global
gcloud iam workload-identity-pools providers create-oidc personal-site --project $RUN \
  --location global --workload-identity-pool github \
  --issuer-uri https://token.actions.githubusercontent.com \
  --attribute-mapping 'google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref' \
  --attribute-condition "assertion.repository == '$REPO' && assertion.ref == 'refs/heads/main'"
gcloud iam service-accounts add-iam-policy-binding $SA --project $RUN \
  --role roles/iam.workloadIdentityUser \
  --member "principalSet://iam.googleapis.com/projects/$NUMBER/locations/global/workloadIdentityPools/github/attribute.repository/$REPO"

gh variable set GCP_WORKLOAD_IDENTITY_PROVIDER --repo $REPO \
  --body "projects/$NUMBER/locations/global/workloadIdentityPools/github/providers/personal-site"
gh variable set GCP_DEPLOY_SERVICE_ACCOUNT --repo $REPO --body $SA
gh variable set DEPLOY_ENABLED --repo $REPO --body true
```

If the VM does not use OS Login (`enable-oslogin=TRUE` in instance or project
metadata), grant `roles/compute.instanceAdmin.v1` on `$SITE` instead of
`compute.osAdminLogin`, so `gcloud compute ssh` can add its key. The first run
requires `/srv/zachbednarke.com/current` to be a symlink to the live release; the
script refuses to switch otherwise.
