#!/usr/bin/env bash
# Check that a Cloud Run service is not serving something this commit lacks.
# Runs on the deploy runner, in a checkout with full history:
#
#   check-cloud-run-commit.sh <service> <commit being deployed> <deployer> -- <gcloud flags>...
#
# Every revision serving traffic is read. The Deploy workflow labels each
# revision commit-sha=<commit>; that commit must be an ancestor of (or equal
# to) the one being deployed (deploy/check-deployed-commit.sh). A revision with
# no label is from before the label existed and only warns, so the next deploy
# goes ahead and labels it.
#
# Labels are not enough alone: a `gcloud run deploy` from anywhere else keeps
# the service's revision template, label included. So a revision whose
# serving.knative.dev/creator is not <deployer> (the deploy service account)
# was made out of band and fails, label or not, unless its image is one a
# revision by <deployer> also runs: then only its settings were changed by hand
# (environment, secrets), the code is the workflow's, and the label check
# applies. ALLOW_OVERWRITE=true turns the failures into warnings.
set -euo pipefail

[[ $# -ge 4 && $4 == -- ]] || { echo "usage: $0 <service> <commit being deployed> <deployer> -- <gcloud flags>..." >&2; exit 2; }
service=$1 target=$2 deployer=$3; shift 4
flags=("$@")
here=$(dirname "$0")
allow=${ALLOW_OVERWRITE:-false}

if ! svc=$(gcloud run services describe "$service" "${flags[@]}" --format json); then
  echo "::error::Could not read Cloud Run service $service; cannot check what it serves."
  [[ $allow == true ]] && exit 0
  exit 1
fi
mapfile -t revisions < <(jq -r '[.status.traffic[]? | select((.percent // 0) > 0) | .revisionName // empty] | unique | .[]' <<<"$svc")
if [[ ${#revisions[@]} -eq 0 ]]; then
  echo "::warning::$service: no revision is serving traffic; nothing to check."
  exit 0
fi

status=0
for revision in "${revisions[@]}"; do
  rev=$(gcloud run revisions describe "$revision" "${flags[@]}" --format json)
  sha=$(jq -r '.metadata.labels["commit-sha"] // ""' <<<"$rev")
  creator=$(jq -r '.metadata.annotations["serving.knative.dev/creator"] // ""' <<<"$rev")
  echo "$service: serving $revision (commit-sha=${sha:-none}, created by ${creator:-unknown})"
  if [[ -n $creator && -n $deployer && ${creator,,} != "${deployer,,}" ]]; then
    image=$(jq -r '.status.imageDigest // ""' <<<"$rev")
    if [[ -n $image ]] && gcloud run revisions list --service "$service" "${flags[@]}" --format json \
        | jq -e --arg image "$image" --arg deployer "${deployer,,}" \
          'any(.[]; (.status.imageDigest // "") == $image and ((.metadata.annotations["serving.knative.dev/creator"] // "") | ascii_downcase) == $deployer)' > /dev/null; then
      echo "$service: $revision was made by $creator but runs an image this workflow deployed ($image); only its settings changed."
      MISSING=warn bash "$here/check-deployed-commit.sh" "Cloud Run $service ($revision)" "$sha" "$target" || status=1
      continue
    fi
    msg="revision $revision was deployed by $creator, not by this workflow ($deployer)${sha:+; its commit-sha label $sha may be carried over from the template}."
    if [[ $allow == true ]]; then
      echo "::warning title=Overwriting out-of-band deploy::Cloud Run $service: $msg Overwriting anyway (allow_overwrite_out_of_band=true)."
      continue
    fi
    echo "::error title=Production is ahead of this commit::Cloud Run $service: $msg"
    cat >&2 <<EOF
The live Cloud Run $service was deployed from outside this workflow and may
serve code that is not in main. Deploying would silently replace it. Merge it
into main first, or rerun from the Actions tab: Deploy, Run workflow, with
allow_overwrite_out_of_band checked.
EOF
    status=1
    continue
  fi
  MISSING=warn bash "$here/check-deployed-commit.sh" "Cloud Run $service ($revision)" "$sha" "$target" || status=1
done
exit $status
