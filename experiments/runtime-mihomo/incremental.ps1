param(
    [string]$GoExe = 'go',
    [string]$PythonExe = 'python',
    [ValidateSet('build','lifecycle','compare')][string]$Phase = 'build',
    [string]$PrivateSubscription = ''
)
$ErrorActionPreference = 'Stop'
$taskRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$taskOutput = Join-Path $taskRoot 'artifacts/issue14/incremental'
$taskOldToolchain = $env:GOTOOLCHAIN
$taskOldCGO = $env:CGO_ENABLED
try {
    $env:GOTOOLCHAIN = 'local'
    $env:CGO_ENABLED = '0'
    if ((& $GoExe version) -notmatch 'go1\.25\.0 windows/amd64') { throw 'Requires Go 1.25.0 windows/amd64.' }
    New-Item -ItemType Directory -Force -Path $taskOutput | Out-Null
    Push-Location $PSScriptRoot
    try {
        if ($Phase -eq 'build') {
            & $GoExe test -mod=readonly -tags=no_easytier ./probe -count=1
            if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
            & $GoExe vet -mod=readonly -tags=no_easytier ./probe
            if ($LASTEXITCODE -ne 0) { throw 'Vet failed.' }
            & $GoExe vet -mod=readonly '-tags=no_easytier,external_only' ./probe
            if ($LASTEXITCODE -ne 0) { throw 'External vet failed.' }
            & $GoExe build -mod=readonly -trimpath -tags=no_easytier -o (Join-Path $taskOutput 'B.exe') ./probe
            if ($LASTEXITCODE -ne 0) { throw 'Embedded build failed.' }
            & $GoExe build -mod=readonly -trimpath '-tags=no_easytier,external_only' -o (Join-Path $taskOutput 'C.exe') ./probe
            if ($LASTEXITCODE -ne 0) { throw 'External build failed.' }
            & $GoExe build -mod=readonly -trimpath -tags=no_easytier -ldflags '-X github.com/metacubex/mihomo/constant.Version=v1.19.32' -o (Join-Path $taskOutput 'core.exe') github.com/metacubex/mihomo
            if ($LASTEXITCODE -ne 0) { throw 'Core build failed.' }
            & $GoExe build -mod=readonly -trimpath -tags=no_easytier -o (Join-Path $taskOutput 'fixture.exe') still-wanna-dance/internal/proxyengine/mihomo/testfixture
            if ($LASTEXITCODE -ne 0) { throw 'Fixture build failed.' }
        } else {
            $taskArgs = @((Join-Path $PSScriptRoot 'incremental.py'), '--bin', $taskOutput, '--phase', $Phase)
            if ($PrivateSubscription) { $taskArgs += @('--private-subscription', (Resolve-Path -LiteralPath $PrivateSubscription).Path) }
            & $PythonExe @taskArgs
            if ($LASTEXITCODE -ne 0) { throw 'Checks failed; preserve and inspect results.' }
        }
    } finally { Pop-Location }
} finally {
    $env:GOTOOLCHAIN = $taskOldToolchain
    $env:CGO_ENABLED = $taskOldCGO
}
