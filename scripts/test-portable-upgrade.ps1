param(
    [Parameter(Mandatory = $true)][string]$PreviousZip,
    [Parameter(Mandatory = $true)][string]$Zip,
    [string]$EvidenceDir = (Join-Path (Split-Path -Parent $PSScriptRoot) 'artifacts/portable-upgrade')
)
$ErrorActionPreference = 'Stop'
$before = (Resolve-Path -LiteralPath $PreviousZip).Path
$after = (Resolve-Path -LiteralPath $Zip).Path
$evidencePath = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($EvidenceDir)
$root = Join-Path $evidencePath ([guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root -Force | Out-Null
$oldBefore = $env:SWD_UPGRADE_BEFORE_EXE
$oldAfter = $env:SWD_UPGRADE_AFTER_EXE
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    # Both ZIPs are checksum/layout checked before extracting executable test inputs.
    & "$PSScriptRoot/test-portable.ps1" -PreviousZip $before -Zip $after -EvidenceDir $root
    Expand-Archive -LiteralPath $before -DestinationPath (Join-Path $root 'before')
    Expand-Archive -LiteralPath $after -DestinationPath (Join-Path $root 'after')
    $env:SWD_UPGRADE_BEFORE_EXE = Join-Path $root 'before/SWD/SWD.exe'
    $env:SWD_UPGRADE_AFTER_EXE = Join-Path $root 'after/SWD/SWD.exe'
    & "$PSScriptRoot/with-test-temp.ps1" go test -tags=no_easytier ./internal/console -run '^TestPortableExecutableUpgrade$' -count=1 -v 2>&1 | Tee-Object -FilePath (Join-Path $root 'persisted-data.log')
    if ($LASTEXITCODE -ne 0) { throw 'Persisted-data upgrade verification failed.' }
} finally {
    Pop-Location
    $env:SWD_UPGRADE_BEFORE_EXE = $oldBefore
    $env:SWD_UPGRADE_AFTER_EXE = $oldAfter
    Write-Host "Upgrade evidence: $root"
}
