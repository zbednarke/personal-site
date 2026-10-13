#!/usr/bin/env bash
# Send a static release to the site VM over ssh sessions and publish it there,
# checking every byte. Runs on the deploy runner:
#
#   upload-static-release.sh upload <remote dir> <file>... -- <ssh command prefix>...
#   upload-static-release.sh publish <remote dir> <archive> <publish script> <commit sha> -- <ssh command prefix>...
#
# e.g. ... upload /tmp/site-release-<sha> site.tar.gz deploy/publish-static-release.sh \
#        -- gcloud compute ssh VM --tunnel-through-iap ...
# The prefix gets `--command <remote shell command>` appended.
#
# upload bundles the files into one tar and streams it on the ssh session's
# stdin instead of `gcloud compute scp`, which through the IAP tunnel
# intermittently sat about 240 s before exiting whatever the size (31.9 KB in
# run 38338281490). On the VM the remote dir must not be a symlink and must be
# the ssh user's (it is made 700), and the bundle's SHA-256 must equal the local
# one before it is unpacked there; otherwise nothing is unpacked and the attempt
# fails. Each attempt is capped at UPLOAD_TIMEOUT seconds (default 120 s plus
# 1 s per 4 KB of bundle, at most 900 s, for a tunnel at a few KB/s), and there
# are UPLOAD_ATTEMPTS attempts (default 2). An attempt whose session hangs
# after the VM confirmed the hash and unpacked the bundle counts as done. When
# every attempt fails the remote dir is removed (best effort) and the exit
# status is non-zero; the caller must not publish then.
#
# publish re-checks on the VM, before anything runs as root, that the remote
# dir is not a symlink and is the ssh user's, and that the archive and publish
# script there have the runner's SHA-256s; then it runs the publish script with
# sudo and removes the remote dir whatever the outcome. It is capped at
# PUBLISH_TIMEOUT seconds (default 300); a session that hangs after the VM
# reported the release published counts as done.
set -euo pipefail

usage() {
  echo "usage: $0 upload <remote dir> <file>... -- <ssh command prefix>..." >&2
  echo "       $0 publish <remote dir> <archive> <publish script> <commit sha> -- <ssh command prefix>..." >&2
  exit 2
}
[[ $# -ge 5 ]] || usage
mode=$1 dir=$2; shift 2
args=()
while [[ $# -gt 0 && $1 != -- ]]; do args+=("$1"); shift; done
[[ $# -gt 1 && ${#args[@]} -gt 0 ]] || usage
shift
ssh_cmd=("$@")
if [[ ! $dir =~ ^/[A-Za-z0-9._/-]+$ || $dir == *..* ]]; then
  echo "Invalid remote dir: $dir" >&2
  exit 2
fi
files=("${args[@]}")
[[ $mode == publish && ${#args[@]} -eq 3 ]] && files=("${args[@]:0:2}")
for f in "${files[@]}"; do
  [[ -f $f ]] || { echo "Missing file: $f" >&2; exit 2; }
  if [[ ! $(basename "$f") =~ ^[A-Za-z0-9._-]+$ ]]; then echo "Invalid file name: $f" >&2; exit 2; fi
done
sha256() { sha256sum "$1" | cut -d' ' -f1; }
is_timeout() { [[ $1 -eq 124 || $1 -eq 137 ]]; }
work=
trap '[[ -z $work ]] || rm -rf "$work"' EXIT

# Leaves nothing behind unless the dir is a symlink or someone else's.
cleanup_remote="d='$dir'; if [ ! -L \"\$d\" ] && [ -O \"\$d\" ]; then rm -rf \"\$d\"; fi"

upload() {
  local bundle expected size timeout_s attempts attempt remote t status out elapsed received reason
  work=$(mktemp -d)
  local names=()
  for f in "${args[@]}"; do cp "$f" "$work/"; names+=("$(basename "$f")"); done
  bundle=$work/bundle.tar
  tar -cf "$bundle" -C "$work" "${names[@]}"
  expected=$(sha256 "$bundle")
  size=$(wc -c < "$bundle")
  timeout_s=${UPLOAD_TIMEOUT:-$(( 120 + size / 4096 > 900 ? 900 : 120 + size / 4096 ))}
  attempts=${UPLOAD_ATTEMPTS:-2}
  echo "Uploading $size bytes (sha256 $expected), timeout ${timeout_s} s per attempt, $attempts attempts"

  for ((attempt = 1; attempt <= attempts; attempt++)); do
    # POSIX sh for the VM user's login shell.
    remote="set -e; d='$dir'
if [ -L \"\$d\" ]; then echo \"\$d is a symlink; not uploading\" >&2; exit 4; fi
mkdir -p \"\$d\"
if [ -L \"\$d\" ] || [ ! -O \"\$d\" ]; then echo \"\$d is not ours; not uploading\" >&2; exit 4; fi
chmod 700 \"\$d\"
f=\"\$d/bundle.$attempt.tar\"; s=\$(date +%s%N); cat > \"\$f\"; e=\$(date +%s%N)
h=\$(sha256sum \"\$f\" | cut -d' ' -f1)
echo \"received \$(wc -c < \"\$f\") bytes in \$(( (e - s) / 1000000 )) ms\"
if [ \"\$h\" != '$expected' ]; then rm -f \"\$f\"; echo \"sha256 mismatch on the VM: \$h\" >&2; exit 3; fi
tar -xf \"\$f\" -C \"\$d\" --no-same-owner; rm -f \"\$f\"; echo \"verified sha256 \$h\""
    t=$SECONDS
    status=0
    out=$(timeout -k 10 "$timeout_s" "${ssh_cmd[@]}" --command "$remote" < "$bundle") || status=$?
    elapsed=$((SECONDS - t))
    received=$(grep -m1 '^received ' <<<"$out" || true)
    # The line is printed only after the hash matched and tar succeeded under
    # set -e, so a session that then hangs on close has done its work.
    if grep -qxF "verified sha256 $expected" <<<"$out" && { [[ $status -eq 0 ]] || is_timeout "$status"; }; then
      local note=
      is_timeout "$status" && note=" (the session did not close; timed out after ${timeout_s} s)"
      echo "Upload attempt $attempt: $size bytes in $elapsed s (VM ${received:-reported nothing}); sha256 $expected verified on the VM$note"
      return 0
    fi
    if is_timeout "$status"; then
      reason="timed out after ${timeout_s} s"
    elif [[ $status -eq 0 ]]; then
      reason="the VM did not confirm sha256 $expected"
    else
      reason="exit $status"
    fi
    echo "Upload attempt $attempt of $attempts failed after $elapsed s: $reason (VM ${received:-reported nothing})" >&2
  done
  echo "Upload failed after $attempts attempts; not publishing. Removing $dir on the VM." >&2
  timeout -k 5 $(( timeout_s < 60 ? timeout_s : 60 )) "${ssh_cmd[@]}" --command "$cleanup_remote" < /dev/null > /dev/null || true
  return 1
}

publish() {
  [[ ${#args[@]} -eq 3 ]] || usage
  local archive=${args[0]} script=${args[1]} sha=${args[2]}
  [[ $sha =~ ^[0-9a-f]{40}$ ]] || { echo "Invalid commit: $sha" >&2; exit 2; }
  local a sname timeout_s remote status=0 out t
  a=$(basename "$archive"); sname=$(basename "$script")
  timeout_s=${PUBLISH_TIMEOUT:-300}
  # Everything sudo runs is checked first: the dir is ours and not a symlink
  # (so only this user, who can sudo anyway, could have changed it), and the
  # files are the runner's bytes.
  remote="d='$dir'
if [ -L \"\$d\" ] || [ ! -d \"\$d\" ] || [ ! -O \"\$d\" ]; then echo \"\$d is missing, a symlink or not ours; not publishing\" >&2; exit 4; fi
if ! printf '%s  %s\n' '$(sha256 "$script")' \"\$d/$sname\" '$(sha256 "$archive")' \"\$d/$a\" | sha256sum -c --quiet - >&2; then
  echo \"The files in \$d are not the runner's; not publishing\" >&2; rm -rf \"\$d\"; exit 3
fi
s=0; sudo bash \"\$d/$sname\" \"\$d/$a\" '$sha' || s=\$?
rm -rf \"\$d\"
if [ \$s -eq 0 ]; then echo 'published $sha'; fi
exit \$s"
  t=$SECONDS
  out=$(timeout -k 10 "$timeout_s" "${ssh_cmd[@]}" --command "$remote" < /dev/null) || status=$?
  [[ -n $out ]] && printf '%s\n' "$out"
  if grep -qxF "published $sha" <<<"$out" && { [[ $status -eq 0 ]] || is_timeout "$status"; }; then
    is_timeout "$status" && echo "The publish session did not close; timed out after ${timeout_s} s, but the VM reported the release published."
    return 0
  fi
  if is_timeout "$status"; then
    # Not cleaned up from here: the publish may still be running, and the
    # remote command removes the dir when it ends.
    echo "Publish timed out after ${timeout_s} s without the VM reporting the release published." >&2
  else
    echo "Publish failed (exit $status) after $((SECONDS - t)) s." >&2
  fi
  return 1
}

case $mode in
  upload) upload ;;
  publish) publish ;;
  *) usage ;;
esac
