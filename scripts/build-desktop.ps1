param([string]$Output = 'bin/SWD.exe')
$ErrorActionPreference = 'Stop'
$previous = @{}
foreach ($key in @('GOOS', 'GOARCH', 'GOAMD64', 'CGO_ENABLED', 'GOFLAGS', 'GOEXPERIMENT', 'GO111MODULE', 'GOWORK', 'GOTOOLCHAIN', 'GOENV')) {
    $previous[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:GOAMD64 = 'v1'
    $env:CGO_ENABLED = '0'
    $env:GOFLAGS = ''
    $env:GOEXPERIMENT = ''
    $env:GO111MODULE = 'on'
    $env:GOWORK = 'off'
    $env:GOTOOLCHAIN = 'local'
    $env:GOENV = 'off'
    go build -tags=no_easytier -trimpath -mod=readonly -ldflags '-s -w -H=windowsgui' -o $Output ./cmd/still-wanna-dance-console
    if ($LASTEXITCODE -ne 0) { throw 'Desktop build failed.' }
    Write-Host "Built $Output (Windows tray, no console window)."
} finally {
    Pop-Location
    foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process') }
}
