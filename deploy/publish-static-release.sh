#!/usr/bin/env bash
# Publish a static site release on the site VM. Runs as root via the deploy
# workflow: publish-static-release.sh <site.tar.gz> <commit sha>
#
# The new release starts as a copy of the live one and the archive is overlaid
# on it, so files the repository does not manage are kept (the documented
# overlay practice). `current` is switched atomically; the previous release is
# left in place for rollback and older workflow releases are pruned.
set -euo pipefail

archive=$1
sha=$2
root=${SITE_ROOT:-/srv/zachbednarke.com}
current=$root/current
releases=$root/releases

if [[ ! -L $current ]]; then
  echo "$current is not a symlink; refusing to switch releases." >&2
  exit 1
fi
if [[ ! $sha =~ ^[0-9a-f]{40}$ ]]; then
  echo "Invalid commit: $sha" >&2
  exit 1
fi

previous=$(readlink -f "$current")
next=$releases/$(date -u +%Y%m%dT%H%M%SZ)-${sha:0:12}
mkdir -p "$releases"
cp -a "$previous" "$next"
tar -xzf "$archive" -C "$next" --no-same-owner
chmod -R a+rX "$next"
echo "$sha" > "$next/.release-commit"

for required in index.html jazz/index.html assets/jazz/app.js trumpets/index.html commonplace/index.html assets/commonplace/app.js assets/workbench/workbench.js; do
  if [[ ! -s $next/$required ]]; then
    echo "Release is missing $required; leaving $previous live." >&2
    rm -rf "$next"
    exit 1
  fi
done

ln -sfn "$next" "$root/current.next"
mv -T "$root/current.next" "$current"
echo "Live: $next"
echo "Previous: $previous"
echo "Roll back with: sudo ln -sfn '$previous' '$root/current.next' && sudo mv -T '$root/current.next' '$current'"

# Keep the five newest workflow releases plus whatever is live or previous.
keep=$(ls -1dt "$releases"/*/ 2>/dev/null | head -n 5 || true)
for release in "$releases"/*/; do
  release=${release%/}
  [[ $release == "$next" || $release == "$previous" ]] && continue
  grep -qxF "$release/" <<<"$keep" && continue
  rm -rf "$release"
done
rm -f "$archive"
