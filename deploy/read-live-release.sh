#!/usr/bin/env bash
# Read what the live static release is, in one ssh session. Runs on the
# deploy runner:
#
#   read-live-release.sh <live release dir> <path>... -- <ssh command prefix>...
#
# Writes, in the current directory:
#   live-commit.txt  the release's .release-commit ("missing" when it has none;
#                    publish-static-release.sh writes it, so a release without
#                    one was not published by the Deploy workflow)
#   live-files.txt   every file and symlink in the release, relative
#   live.sha256      sha256sum of the files under <path>... (the changed-files
#                    upload leaves out the ones whose bytes are already live)
# Exits non-zero, with all three empty, when the read fails twice (each try is
# capped at READ_TIMEOUT seconds, default 120).
set -euo pipefail

usage() { echo "usage: $0 <live release dir> <path>... -- <ssh command prefix>..." >&2; exit 2; }
[[ $# -ge 4 ]] || usage
dir=$1; shift
paths=()
while [[ $# -gt 0 && $1 != -- ]]; do paths+=("$1"); shift; done
[[ $# -gt 1 && ${#paths[@]} -gt 0 ]] || usage
shift
ssh_cmd=("$@")
if [[ ! $dir =~ ^/[A-Za-z0-9._/-]+$ || $dir == *..* ]]; then echo "Invalid live release dir: $dir" >&2; exit 2; fi
for p in "${paths[@]}"; do
  [[ $p =~ ^[A-Za-z0-9._][A-Za-z0-9._/-]*$ && $p != *..* ]] || { echo "Invalid path: $p" >&2; exit 2; }
done

# POSIX sh for the VM user's login shell. The end marker tells a complete read
# from a session cut off part way.
remote="cd '$dir' || exit 3
printf 'release-commit %s\n' \"\$(head -c 100 .release-commit 2>/dev/null | tr -d '[:space:]')\"
echo '--- files'; find . -type f -o -type l
echo '--- hashes'; find ${paths[*]} -type f -exec sha256sum {} + 2>/dev/null
echo '--- end'"

: > live-commit.txt; : > live-files.txt; : > live.sha256
out=
for attempt in 1 2; do
  if out=$(timeout -k 10 "${READ_TIMEOUT:-120}" "${ssh_cmd[@]}" --command "$remote" < /dev/null) \
      && grep -qxF -- '--- end' <<<"$out"; then
    break
  fi
  echo "Reading the live release failed (attempt $attempt of 2)." >&2
  out=
done
[[ -n $out ]] || exit 1

# Not necessarily the first line: the VM's login can print a banner first.
commit=$(sed -n 's/^release-commit *//p' <<<"$out" | head -n 1)
echo "${commit:-missing}" > live-commit.txt
awk '/^--- /{part=$2; next} part=="files"' <<<"$out" | sed 's|^\./||' > live-files.txt
awk '/^--- /{part=$2; next} part=="hashes"' <<<"$out" > live.sha256
echo "Live release: commit ${commit:-missing}, $(wc -l < live-files.txt) files, $(wc -l < live.sha256) hashes"
