param([string]$Output = 'bin/stepstash-console.exe')
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    go build -trimpath -ldflags '-H=windowsgui' -o $Output ./cmd/stepstash-console
    if ($LASTEXITCODE -ne 0) { throw 'Desktop build failed.' }
    Write-Host "Built $Output (Windows tray, no console window)."
} finally { Pop-Location }
