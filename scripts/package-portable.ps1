param(
    [ValidatePattern('^[a-zA-Z0-9][a-zA-Z0-9._-]*$')]
    [string]$Version = ('dev-' + (Get-Date -Format 'yyyyMMdd-HHmmss')),
    [string]$OutputDir = 'artifacts'
)
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    # Capture source state before tests/builds can create unignored output directories.
    $revision = & git rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read Git revision.' }
    $changes = & git status --porcelain
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read Git status.' }
    node --test scripts/refresh.test.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Frontend tests failed.' }
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed.' }
    $destination = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDir)
    $name = "StepStash-$Version-windows-amd64-portable"
    $folder = Join-Path $destination $name
    $zip = "$folder.zip"
    foreach ($path in @($folder, $zip, "$zip.sha256")) {
        if (Test-Path -LiteralPath $path) { throw "Output already exists: $path. Choose another version or output directory." }
    }
    New-Item -ItemType Directory -Path $folder -Force | Out-Null
    & "$PSScriptRoot/build-desktop.ps1" -Output (Join-Path $folder 'stepstash-console.exe')
    Copy-Item -LiteralPath 'docs/portable-readme.txt' -Destination (Join-Path $folder 'README.txt')
    Copy-Item -LiteralPath 'LICENSE' -Destination (Join-Path $folder 'LICENSE')
    & "$PSScriptRoot/package-notices.ps1" -Executable (Join-Path $folder 'stepstash-console.exe') -Output (Join-Path $folder 'THIRD-PARTY-NOTICES.txt')
    $goVersion = & go version
    $metadata = [ordered]@{
        version = $Version
        revision = "$revision"
        dirty = [bool]$changes
        builtAtUtc = [DateTime]::UtcNow.ToString('o')
        toolchain = "$goVersion"
        target = 'windows/amd64'
    }
    $metadata | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $folder 'build-info.json') -Encoding UTF8
    Compress-Archive -LiteralPath $folder -DestinationPath $zip
    $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $name.zip" | Set-Content -LiteralPath "$zip.sha256" -Encoding ASCII
    Write-Host "Portable folder: $folder"
    Write-Host "Distribution ZIP: $zip"
    Write-Host "SHA256: $hash"
} finally { Pop-Location }
