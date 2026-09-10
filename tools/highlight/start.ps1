$ErrorActionPreference = 'Stop'
$workerPath = Join-Path $PSScriptRoot 'worker.py'
$dataPath = Join-Path $PSScriptRoot '../../.local/highlights'
foreach ($dependency in @('python', 'node', 'codex', 'ffmpeg', 'ffprobe')) {
    if (-not (Get-Command $dependency -ErrorAction SilentlyContinue)) { throw "Missing dependency: $dependency" }
}
python $workerPath --data $dataPath
