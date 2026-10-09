#!/usr/bin/env bash
# One-time setup for .github/workflows/deploy.yml. Run it where gcloud is signed
# in as a project owner, e.g. Google Cloud Shell:
#   bash deploy/setup-deploy-auth.sh
# Safe to re-run: existing resources are kept. Prints the GitHub repository
# variables to set at the end.
set -euo pipefail

RUN=parabolio-prod
SITE=actual-budget-zb
ZONE=us-west1-a
VM=actual-server
REPO=zbednarke/personal-site
NUMBER=$(gcloud projects describe "$RUN" --format='value(projectNumber)')
SA=github-deploy@$RUN.iam.gserviceaccount.com
PROVIDER=projects/$NUMBER/locations/global/workloadIdentityPools/github/providers/personal-site

exists() { "$@" >/dev/null 2>&1; }

echo "== Service account"
exists gcloud iam service-accounts describe "$SA" --project "$RUN" ||
  gcloud iam service-accounts create github-deploy --project "$RUN" --display-name "GitHub deploy"

echo "== Roles: Cloud Run source deploys and pre-deploy backups ($RUN)"
for role in run.admin iam.serviceAccountUser cloudbuild.builds.editor artifactregistry.writer storage.admin serviceusage.serviceUsageConsumer cloudsql.editor; do
  gcloud projects add-iam-policy-binding "$RUN" --member "serviceAccount:$SA" --role "roles/$role" --condition None --quiet >/dev/null
done

echo "== Roles: SSH to the site VM through IAP ($SITE)"
oslogin=$(gcloud compute instances describe "$VM" --project "$SITE" --zone "$ZONE" \
  --format='value(metadata.items.filter(key:enable-oslogin).extract(value).flatten())' 2>/dev/null || true)
[[ -z $oslogin ]] && oslogin=$(gcloud compute project-info describe --project "$SITE" \
  --format='value(commonInstanceMetadata.items.filter(key:enable-oslogin).extract(value).flatten())' 2>/dev/null || true)
if [[ ${oslogin^^} == TRUE ]]; then
  login_role=compute.osAdminLogin
else
  echo "   OS Login is off on $VM; granting instance admin so gcloud can add an SSH key."
  login_role=compute.instanceAdmin.v1
fi
for role in iap.tunnelResourceAccessor compute.viewer "$login_role"; do
  gcloud projects add-iam-policy-binding "$SITE" --member "serviceAccount:$SA" --role "roles/$role" --condition None --quiet >/dev/null
done
vm_sa=$(gcloud compute instances describe "$VM" --project "$SITE" --zone "$ZONE" --format='value(serviceAccounts[0].email)')
if [[ -n $vm_sa ]]; then
  gcloud iam service-accounts add-iam-policy-binding "$vm_sa" --project "$SITE" \
    --member "serviceAccount:$SA" --role roles/iam.serviceAccountUser --quiet >/dev/null
fi

echo "== Workload Identity Federation (only $REPO main)"
exists gcloud iam workload-identity-pools describe github --project "$RUN" --location global ||
  gcloud iam workload-identity-pools create github --project "$RUN" --location global --display-name "GitHub Actions"
exists gcloud iam workload-identity-pools providers describe personal-site --project "$RUN" --location global --workload-identity-pool github ||
  gcloud iam workload-identity-pools providers create-oidc personal-site --project "$RUN" \
    --location global --workload-identity-pool github \
    --issuer-uri https://token.actions.githubusercontent.com \
    --attribute-mapping 'google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref' \
    --attribute-condition "assertion.repository == '$REPO' && assertion.ref == 'refs/heads/main'"
gcloud iam service-accounts add-iam-policy-binding "$SA" --project "$RUN" \
  --role roles/iam.workloadIdentityUser \
  --member "principalSet://iam.googleapis.com/projects/$NUMBER/locations/global/workloadIdentityPools/github/attribute.repository/$REPO" --quiet >/dev/null

echo "== Checking the live site release"
target=$(gcloud compute ssh "$VM" --project "$SITE" --zone "$ZONE" --tunnel-through-iap --quiet \
  --command 'if [ -L /srv/zachbednarke.com/current ]; then readlink -f /srv/zachbednarke.com/current; else echo NOT-A-SYMLINK; fi' 2>/dev/null || echo UNREACHABLE)
echo "   current -> $target"
if [[ $target == NOT-A-SYMLINK ]]; then
  echo "   /srv/zachbednarke.com/current must be a symlink to the live release before the first deploy." >&2
fi

sql=$(gcloud sql instances list --project "$RUN" --format='value(name)' 2>/dev/null | head -n 1 || true)

cat <<EOF

Done. In GitHub: Settings > Secrets and variables > Actions > Variables, add:
  GCP_WORKLOAD_IDENTITY_PROVIDER = $PROVIDER
  GCP_DEPLOY_SERVICE_ACCOUNT     = $SA
  DEPLOY_ENABLED                 = true
${sql:+  JAZZ_CLOUD_SQL_INSTANCE        = $sql   (optional: back up before each deploy)}
Then Actions > Deploy > Run workflow.
EOF
