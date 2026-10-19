param([string]$Output = 'bin/SWD.exe', [string]$Version = 'dev')
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    node scripts/check-webui.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Embedded Web UI is stale. Run pnpm --dir webui build.' }
    node scripts/build-desktop.mjs $Output $Version
    if ($LASTEXITCODE -ne 0) { throw 'Desktop build failed.' }
    Write-Host "Built $Output (Windows tray, no console window)."
} finally { Pop-Location }
