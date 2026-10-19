param(
    [ValidatePattern('^[a-zA-Z0-9][a-zA-Z0-9._-]*$')]
    [string]$Version = ('dev-' + (Get-Date -Format 'yyyyMMdd-HHmmss')),
    [string]$OutputDir = 'artifacts',
    [string]$ReleaseTag,
    [string]$PreviousZip
)
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    if ($ReleaseTag) {
        if ($PSBoundParameters.ContainsKey('Version')) { throw 'Use ReleaseTag alone for a formal package.' }
        $releaseArgs = @('scripts/release.mjs', 'create', '--tag', $ReleaseTag, '--output', $OutputDir)
        if ($PreviousZip) { $releaseArgs += @('--previous-portable', (Resolve-Path -LiteralPath $PreviousZip).Path) }
        & "$PSScriptRoot/with-test-temp.ps1" node @releaseArgs
        if ($LASTEXITCODE -ne 0) { throw 'Formal release checks failed.' }
        return
    }
    if ($Version -match '^v?\d+\.\d+\.\d+(?:[-+].*)?$') { throw 'A versioned release requires -ReleaseTag and matching corresponding source. Use dev-/ci- labels for local builds.' }
    # Capture source state before tests/builds can create unignored output directories.
    $revision = & git rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read Git revision.' }
    $changes = & git status --porcelain
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read Git status.' }
    node scripts/check-webui.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Embedded Web UI is stale. Rebuild webui before packaging.' }
    & "$PSScriptRoot/with-test-temp.ps1" go test -tags=no_easytier ./cmd/... ./internal/...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    & "$PSScriptRoot/with-test-temp.ps1" go vet -tags=no_easytier ./cmd/... ./internal/...
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed.' }
    $destination = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDir)
    $name = "still-wanna-dance-$Version-windows-amd64-portable"
    $staging = Join-Path $destination $name
    $folder = Join-Path $staging 'SWD'
    $zip = Join-Path $destination "$name.zip"
    foreach ($path in @($staging, $zip, "$zip.sha256")) {
        if (Test-Path -LiteralPath $path) { throw "Output already exists: $path. Choose another version or output directory." }
    }
    $licenses = Join-Path $folder 'licenses'
    New-Item -ItemType Directory -Path $licenses -Force | Out-Null
    & "$PSScriptRoot/build-desktop.ps1" -Output (Join-Path $folder 'SWD.exe') -Version $Version
    Copy-Item -LiteralPath 'LICENSE' -Destination (Join-Path $licenses 'LICENSE')
    Copy-Item -LiteralPath 'LICENSE-MIT' -Destination (Join-Path $licenses 'LICENSE-MIT')
    Copy-Item -LiteralPath 'LICENSING.md' -Destination (Join-Path $licenses 'LICENSING.md')
    & "$PSScriptRoot/package-notices.ps1" -Executable (Join-Path $folder 'SWD.exe') -Output (Join-Path $licenses 'THIRD-PARTY-NOTICES.txt')
    $metadata = (& (Join-Path $folder 'SWD.exe') -build-info | ConvertFrom-Json)
    if ($LASTEXITCODE -ne 0 -or $metadata.kind -cne 'development' -or $metadata.version -cne $Version -or $metadata.revision -cne "$revision" -or $metadata.dirty -ne [bool]$changes) { throw 'Executable build identity differs from packaging inputs.' }
    [ordered]@{ kind = 'development'; version = $Version; revision = "$revision"; dirty = [bool]$changes } |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $licenses 'SOURCE.json') -Encoding UTF8
    "Development build: $Version`nCommit: $revision`nThis is not a formal distribution. No official corresponding-source asset is claimed.`nPrepare formal releases using scripts/package-portable.ps1 -ReleaseTag and deliver the generated complete source archive. See docs/corresponding-source.md and LICENSING.md." |
        Set-Content -LiteralPath (Join-Path $licenses 'SOURCE.txt') -Encoding UTF8
    Compress-Archive -LiteralPath $folder -DestinationPath $zip
    $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
    "$hash  $name.zip" | Set-Content -LiteralPath "$zip.sha256" -Encoding ASCII
    if ($PreviousZip) { & "$PSScriptRoot/test-portable-upgrade.ps1" -PreviousZip $PreviousZip -Zip $zip }
    Write-Host "Portable folder: $folder"
    Write-Host "Distribution ZIP: $zip"
    Write-Host "SHA256: $hash"
} finally { Pop-Location }
