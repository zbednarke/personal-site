#!/usr/bin/env bash
# Run by upload-static-release.sh after it verifies the archive and this script.
# Keep maintenance tools outside the public static release.
set -euo pipefail
archive=$1
sha=$2
[[ $sha =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid commit' >&2; exit 1; }
work=$(mktemp -d /tmp/686-config.XXXXXX)
trap 'rm -rf "$work"' EXIT
tar -xzf "$archive" -C "$work" --no-same-owner
python3 "$work/install-686-route.py"
