#!/usr/bin/env bash
# Upload release files to the site VM over one ssh session and verify them
# there. Runs on the deploy runner:
#
#   upload-static-release.sh <remote dir> <file>... -- <ssh command prefix>...
#
# e.g. upload-static-release.sh /tmp/site-release-<sha> site.tar.gz \
#        deploy/publish-static-release.sh -- gcloud compute ssh VM --tunnel-through-iap ...
#
# The files are bundled into one tar that is streamed on the ssh session's
# stdin (`--command 'cat > ...'`) instead of `gcloud compute scp`, which through
# the IAP tunnel intermittently sat about 240 s before exiting whatever the
# size (31.9 KB in run 38338281490). On the VM the bundle's SHA-256 must equal
# the local one before it is unpacked into <remote dir>; otherwise nothing is
# unpacked and the attempt fails. Each attempt is capped by UPLOAD_TIMEOUT
# seconds (default 120) and there are UPLOAD_ATTEMPTS attempts (default 2), so
# a stall costs about two minutes, not four. Exits non-zero when every attempt
# fails; the caller must not publish then.
set -euo pipefail

usage() { echo "usage: $0 <remote dir> <file>... -- <ssh command prefix>..." >&2; exit 2; }
[[ $# -ge 4 ]] || usage
dir=$1; shift
files=()
while [[ $# -gt 0 && $1 != -- ]]; do files+=("$1"); shift; done
[[ $# -gt 0 && ${#files[@]} -gt 0 ]] || usage
shift
ssh_cmd=("$@")
[[ ${#ssh_cmd[@]} -gt 0 ]] || usage
if [[ ! $dir =~ ^/[A-Za-z0-9._/-]+$ ]]; then
  echo "Invalid remote dir: $dir" >&2
  exit 2
fi
timeout_s=${UPLOAD_TIMEOUT:-120}
attempts=${UPLOAD_ATTEMPTS:-2}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for f in "${files[@]}"; do
  [[ -f $f ]] || { echo "Missing file: $f" >&2; exit 2; }
  cp "$f" "$work/"
done
names=()
for f in "${files[@]}"; do names+=("$(basename "$f")"); done
bundle=$work/bundle.tar
tar -cf "$bundle" -C "$work" "${names[@]}"
expected=$(sha256sum "$bundle" | cut -d' ' -f1)
size=$(wc -c < "$bundle")

for ((attempt = 1; attempt <= attempts; attempt++)); do
  # POSIX sh for the VM user's login shell. The bundle is unpacked only when
  # its hash is the one computed here.
  remote="set -e; d='$dir'; mkdir -p -m 700 \"\$d\"; [ -O \"\$d\" ] || { echo \"\$d is not ours\" >&2; exit 4; }
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
  if [[ $status -eq 0 ]] && grep -qxF "verified sha256 $expected" <<<"$out"; then
    echo "Upload attempt $attempt: $size bytes in $elapsed s (VM ${received:-reported nothing}); sha256 $expected verified on the VM"
    exit 0
  fi
  if [[ $status -eq 124 || $status -eq 137 ]]; then
    reason="timed out after ${timeout_s} s"
  elif [[ $status -eq 0 ]]; then
    reason="the VM did not confirm sha256 $expected"
  else
    reason="exit $status"
  fi
  echo "Upload attempt $attempt of $attempts failed after $elapsed s: $reason (VM ${received:-reported nothing})" >&2
done
echo "Upload failed after $attempts attempts; not publishing." >&2
exit 1
