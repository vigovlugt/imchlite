$ErrorActionPreference = "Stop"

$repo = "vigovlugt/imchlite"

$releases = Invoke-RestMethod "https://api.github.com/repos/$repo/releases"
$asset = $releases[0].assets | Where-Object { $_.name -like "*windows-amd64.zip" } | Select-Object -First 1

if (-not $asset) { throw "no release asset found" }

Invoke-WebRequest $asset.browser_download_url -OutFile $asset.name
