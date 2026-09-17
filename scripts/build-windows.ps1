$ErrorActionPreference = "Stop"

Push-Location frontend
try {
    bun install --frozen-lockfile
    bun run build
} finally {
    Pop-Location
}

$env:CGO_ENABLED = "1"
go build .

if ($LASTEXITCODE -ne 0) {
    throw "go build failed"
}

Write-Host "Built imchlite.exe"
