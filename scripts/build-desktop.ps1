param([string]$Output = 'bin/still-wanna-dance-console.exe')
$ErrorActionPreference = 'Stop'
$previous = @{}
foreach ($key in @('GOOS', 'GOARCH', 'CGO_ENABLED')) {
    $previous[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    go build -trimpath -ldflags '-s -w -H=windowsgui' -o $Output ./cmd/still-wanna-dance-console
    if ($LASTEXITCODE -ne 0) { throw 'Desktop build failed.' }
    Write-Host "Built $Output (Windows tray, no console window)."
} finally {
    Pop-Location
    foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process') }
}
