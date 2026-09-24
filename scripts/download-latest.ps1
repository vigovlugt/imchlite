$ErrorActionPreference = "Stop"

$repo = "vigovlugt/imchlite"

if ($env:PROCESSOR_ARCHITECTURE -ne "AMD64") {
    throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE (only amd64)"
}

$releases = Invoke-RestMethod "https://api.github.com/repos/$repo/releases"
$asset = $releases[0].assets | Where-Object { $_.name -like "*windows-amd64.zip" } | Select-Object -First 1

if (-not $asset) { throw "no release asset found" }

Invoke-WebRequest $asset.browser_download_url -OutFile $asset.name
Expand-Archive $asset.name -DestinationPath . -Force
Remove-Item $asset.name

Write-Host "Downloaded imchlite.exe"
