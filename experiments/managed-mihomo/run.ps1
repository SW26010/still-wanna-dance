param(
    [string]$GoExe = 'go',
    [string]$PythonExe = 'python',
    [string]$PrivateSubscription = ''
)
$ErrorActionPreference = 'Stop'
$taskRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$taskOutput = Join-Path $taskRoot 'artifacts/issue14/managed-core-prototype'
$taskOldToolchain = $env:GOTOOLCHAIN
$taskOldCGO = $env:CGO_ENABLED
try {
    $env:GOTOOLCHAIN = 'local'
    $env:CGO_ENABLED = '0'
    if ((& $GoExe version) -notmatch 'go1\.25\.0 windows/amd64') {
        throw 'This evidence baseline requires Go 1.25.0 windows/amd64. Pass -GoExe explicitly.'
    }
    $taskModCache = & $GoExe env GOMODCACHE
    $taskCoreSource = Join-Path $taskModCache 'github.com/metacubex/mihomo@v1.19.32'
    if (-not (Test-Path -LiteralPath (Join-Path $taskCoreSource 'main.go'))) {
        throw 'Pinned Mihomo v1.19.32 source must already be present in the module cache.'
    }
    New-Item -ItemType Directory -Force -Path $taskOutput | Out-Null
    Push-Location $taskCoreSource
    try {
        & $GoExe build -mod=readonly -trimpath -tags=no_easytier -ldflags '-X github.com/metacubex/mihomo/constant.Version=v1.19.32' -o (Join-Path $taskOutput 'core-verified.exe') .
        if ($LASTEXITCODE -ne 0) { throw 'Core build failed.' }
    } finally { Pop-Location }
    Push-Location $taskRoot
    try {
        & $GoExe build -tags=no_easytier -o (Join-Path $taskOutput 'probe.exe') ./experiments/managed-mihomo/probe
        if ($LASTEXITCODE -ne 0) { throw 'Probe build failed.' }
        & $GoExe build -tags=no_easytier -o (Join-Path $taskOutput 'fixture.exe') ./internal/proxyengine/mihomo/testfixture
        if ($LASTEXITCODE -ne 0) { throw 'Fixture build failed.' }
        $taskArgs = @((Join-Path $PSScriptRoot 'run.py'), '--core', (Join-Path $taskOutput 'core-verified.exe'), '--probe', (Join-Path $taskOutput 'probe.exe'), '--fixture', (Join-Path $taskOutput 'fixture.exe'))
        if ($PrivateSubscription) { $taskArgs += @('--private-subscription', (Resolve-Path -LiteralPath $PrivateSubscription).Path) }
        & $PythonExe @taskArgs
        if ($LASTEXITCODE -ne 0) { throw 'Validation failed; retain results.json and inspect the failed check.' }
    } finally { Pop-Location }
} finally {
    $env:GOTOOLCHAIN = $taskOldToolchain
    $env:CGO_ENABLED = $taskOldCGO
}
