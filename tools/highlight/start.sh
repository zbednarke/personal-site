#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
command -v ffmpeg >/dev/null
command -v ffprobe >/dev/null
python3 -c 'import numpy' || { echo 'Install analysis dependencies: python3 -m pip install -r tools/highlight/requirements.txt'; exit 1; }
exec python3 tools/highlight/worker.py --data .local/highlights
