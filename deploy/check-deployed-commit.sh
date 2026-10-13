#!/usr/bin/env bash
# Refuse to deploy over something production has that this commit does not.
# Runs on the deploy runner, in a checkout with full history:
#
#   check-deployed-commit.sh <what> <live commit> <commit being deployed>
#
# <live commit> is what production says it is running (the static release's
# .release-commit, or a Cloud Run revision's commit-sha label); pass "" or
# "missing" when it has none. Passes (exit 0) when the live commit is the
# commit being deployed or one of its ancestors. Fails (exit 1) when it is
# missing, unknown to this repository, or not an ancestor: production was then
# deployed from somewhere else, and deploying would silently erase that.
#
# ALLOW_OVERWRITE=true turns every failure into a warning (the Deploy
# workflow's allow_overwrite_out_of_band input). MISSING=warn turns only the
# missing case into a warning (Cloud Run revisions from before revisions were
# labelled).
set -euo pipefail

[[ $# -eq 3 ]] || { echo "usage: $0 <what> <live commit> <commit being deployed>" >&2; exit 2; }
what=$1 live=$2 target=$3
allow=${ALLOW_OVERWRITE:-false}
missing=${MISSING:-fail}

target_sha=$(git rev-parse --verify --quiet "$target^{commit}") || { echo "Not a commit in this checkout: $target" >&2; exit 2; }

refuse() {
  local reason=$1
  if [[ $allow == true ]]; then
    echo "::warning title=Overwriting out-of-band deploy::$what: $reason Overwriting anyway (allow_overwrite_out_of_band=true)."
    return 0
  fi
  echo "::error title=Production is ahead of this commit::$what: $reason"
  cat >&2 <<EOF
Refusing to deploy $target_sha over the live $what.
$reason
The live $what has commits or files that are not in main. Deploying would
silently erase them. Merge them into main first (then this deploy runs again
from that merge), or, if they should be thrown away, rerun from the Actions tab:
Deploy, Run workflow, with allow_overwrite_out_of_band checked.
EOF
  return 1
}

if [[ -z $live || $live == missing ]]; then
  if [[ $missing == warn ]]; then
    echo "::warning title=No deployed commit recorded::$what has no commit recorded (deployed before revisions were labelled); cannot check it, deploying."
    exit 0
  fi
  refuse "it records no deployed commit (no .release-commit), so it was not published by this workflow."
  exit
fi
if [[ ! $live =~ ^[0-9a-f]{40}$ ]] || ! git cat-file -e "$live^{commit}" 2>/dev/null; then
  refuse "it is at ${live:0:80}, which is not a commit in this repository."
  exit
fi
if [[ $live == "$target_sha" ]]; then
  echo "$what: live commit $live is the commit being deployed."
  exit 0
fi
if git merge-base --is-ancestor "$live" "$target_sha"; then
  echo "$what: live commit $live is an ancestor of $target_sha."
  exit 0
fi
refuse "it is at $live, which is not an ancestor of $target_sha."
