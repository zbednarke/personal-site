$ErrorActionPreference = 'Stop'
$workerPath = Join-Path $PSScriptRoot 'worker.py'
$dataPath = Join-Path $PSScriptRoot '../../.local/highlights'
foreach ($dependency in @('python', 'node', 'codex', 'ffmpeg', 'ffprobe')) {
    if (-not (Get-Command $dependency -ErrorAction SilentlyContinue)) { throw "Missing dependency: $dependency" }
}
# The independent publisher also picks up completed jobs after a worker restart.
$publisherPath = Join-Path $PSScriptRoot 'publish.py'
New-Item -ItemType Directory -Force -Path $dataPath | Out-Null
if (-not (Get-NetTCPConnection -LocalPort 8766 -ErrorAction SilentlyContinue)) {
    Start-Process -FilePath (Get-Command python).Source -ArgumentList @("`"$publisherPath`"", '--data', "`"$dataPath`"") -WindowStyle Hidden -RedirectStandardOutput (Join-Path $dataPath 'publisher-stdout.log') -RedirectStandardError (Join-Path $dataPath 'publisher-stderr.log')
}
python $workerPath --data $dataPath --api http://localhost:4173/jazz/api/v1
