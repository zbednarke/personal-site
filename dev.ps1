# -ApiUrl points the pages at another revision of the data service, such as a
# tagged no-traffic Cloud Run revision deployed from a feature branch.
param([string]$ApiUrl = "")

$ErrorActionPreference = "Stop"

$projectRoot = $PSScriptRoot
$apiDirectory = Join-Path $projectRoot "jazz-api"

Write-Host "Connecting the local Jazz and Trumpets pages to the private production data service..."
$gatewayKey = (& gcloud secrets versions access latest --secret=jazz-gateway-key --project=parabolio-prod).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($gatewayKey)) {
    throw "Could not load the local Jazz gateway credential from Google Cloud."
}

if ([string]::IsNullOrWhiteSpace($ApiUrl)) {
    $apiURL = (& gcloud run services describe jazz-api --project=parabolio-prod --region=us-central1 --format="value(status.url)").Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($apiURL)) {
        throw "Could not locate the Jazz data service in Google Cloud."
    }
} else {
    $apiURL = $ApiUrl.Trim()
    Write-Host "Using the data service at $apiURL"
}

$env:JAZZ_DEV_GATEWAY_KEY = $gatewayKey
$env:JAZZ_DEV_API_URL = $apiURL
$env:JAZZ_DEV_USER = "zach"
$env:JAZZ_DEV_STATIC_ROOT = $projectRoot
$env:JAZZ_DEV_ADDR = "localhost:4173"

Push-Location $apiDirectory
try {
    go run ./cmd/jazz-dev
} finally {
    Pop-Location
    Remove-Item Env:JAZZ_DEV_GATEWAY_KEY -ErrorAction SilentlyContinue
    Remove-Item Env:JAZZ_DEV_API_URL -ErrorAction SilentlyContinue
    Remove-Item Env:JAZZ_DEV_USER -ErrorAction SilentlyContinue
    Remove-Item Env:JAZZ_DEV_STATIC_ROOT -ErrorAction SilentlyContinue
    Remove-Item Env:JAZZ_DEV_ADDR -ErrorAction SilentlyContinue
}
