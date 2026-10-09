#!/usr/bin/env bash
# SessionStart hook: sign gcloud in as the claude-agent service account in
# Claude Code cloud sessions. The key comes from the environment variable
# GCLOUD_SA_KEY_B64 (base64 of the JSON key; raw JSON also accepted), which is
# set in the cloud environment's settings and never committed here.
#
# Cloud sessions inject an unusable CLOUDSDK_AUTH_ACCESS_TOKEN that overrides
# any stored account, so on success this also unsets it for later Bash calls
# via CLAUDE_ENV_FILE. Output on stdout is shown to Claude as session context.

[ "${CLAUDE_CODE_REMOTE:-}" = "true" ] || exit 0

say() { echo "[gcloud-auth] $*"; }

if [ -z "${GCLOUD_SA_KEY_B64:-}" ]; then
  say "GCLOUD_SA_KEY_B64 is not set in this environment; gcloud is not signed in."
  exit 0
fi
if ! command -v gcloud >/dev/null 2>&1; then
  say "gcloud is not installed; skipping."
  exit 0
fi

key_dir="$HOME/.config/claude-sa"
key_file="$key_dir/key.json"
mkdir -p "$key_dir" && chmod 700 "$key_dir"
umask 077

raw="$(printf '%s' "$GCLOUD_SA_KEY_B64" | tr -d ' \r\n\t')"
case "$raw" in
  "{"*) printf '%s' "$GCLOUD_SA_KEY_B64" > "$key_file" ;;
  *) printf '%s' "$raw" | base64 -di > "$key_file" 2>/dev/null ;;
esac

if ! python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["type"]=="service_account" and d["private_key"] and d["client_email"]' "$key_file" 2>/dev/null; then
  rm -f "$key_file"
  say "GCLOUD_SA_KEY_B64 did not decode to a service-account JSON key; gcloud is not signed in."
  exit 0
fi

email="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["client_email"])' "$key_file")"
project="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["project_id"])' "$key_file")"

if ! out="$(env -u CLOUDSDK_AUTH_ACCESS_TOKEN gcloud auth activate-service-account --key-file="$key_file" 2>&1)"; then
  say "activating $email failed: $out"
  exit 0
fi
env -u CLOUDSDK_AUTH_ACCESS_TOKEN gcloud config set account "$email" >/dev/null 2>&1
env -u CLOUDSDK_AUTH_ACCESS_TOKEN gcloud config set project "$project" >/dev/null 2>&1

if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
  echo "unset CLOUDSDK_AUTH_ACCESS_TOKEN" >> "$CLAUDE_ENV_FILE"
  echo "export GOOGLE_APPLICATION_CREDENTIALS=\"$key_file\"" >> "$CLAUDE_ENV_FILE"
fi

# gcloud compute ssh/scp need an OpenSSH client, which the image lacks.
if ! command -v ssh >/dev/null 2>&1; then
  (apt-get install -y -qq openssh-client >/dev/null 2>&1 ||
    { apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq openssh-client >/dev/null 2>&1; }) &
fi

say "gcloud signed in as $email (project $project). Use env -u CLOUDSDK_AUTH_ACCESS_TOKEN gcloud ... if a command still reports the injected token."
exit 0
