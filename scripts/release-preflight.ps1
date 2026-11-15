param(
    [ValidateSet('all', 'tools', 'repository', 'product', 'race', 'delivery')][string]$Scope = 'all',
    [string]$ReleaseTag,
    [string]$PreviousZip,
    [string]$PreviousTag,
    [switch]$Representative,
    [ValidateRange(-1, 5)][int]$RaceShard = -1,
    [string]$EvidenceDir = 'artifacts/release-preflight'
)
$ErrorActionPreference = 'Stop'
function Invoke-Checked([string]$Command, [string[]]$CommandArgs) {
    & $Command @CommandArgs
    if ($LASTEXITCODE -ne 0) { throw "$Command failed ($LASTEXITCODE): $($CommandArgs -join ' ')" }
}
function Invoke-Isolated([string[]]$CommandArgs) {
    Invoke-Checked "$PSScriptRoot/with-test-temp.ps1" $CommandArgs
}
function Invoke-Go([string[]]$CommandArgs) {
    Invoke-Isolated (@('node', "$PSScriptRoot/go-patches.mjs") + $CommandArgs)
}
Push-Location (Split-Path -Parent $PSScriptRoot)
$savedEnv = @{}
foreach ($name in @('CGO_ENABLED', 'GOTOOLCHAIN', 'SWD_SETTINGS_BROWSER_EVIDENCE', 'SWD_HOSTS_BROWSER_EVIDENCE')) { $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name) }
$transcript = $false
$record = [ordered]@{ revision = ''; dirty = $false; scope = $Scope; representative = [bool]$Representative; raceShard = $RaceShard; passed = @(); status = 'failed' }
try {
    if (($Scope -eq 'all' -or $Scope -eq 'delivery') -and (!$ReleaseTag -or $Representative)) { throw 'Full local preflight/delivery requires ReleaseTag and full checks.' }
    if ($RaceShard -ne -1 -and $Scope -ne 'race') { throw 'RaceShard applies only to the race scope.' }
    $record.revision = (& git rev-parse HEAD)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read source revision.' }
    $record.dirty = [bool](& git status --porcelain)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read source status.' }
    if ($Scope -eq 'all' -or $Scope -eq 'delivery') {
        if ($ReleaseTag -cnotmatch '^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') { throw 'Invalid release tag.' }
        $tagRevision = & git rev-parse "$ReleaseTag^{commit}"
        if ($LASTEXITCODE -ne 0 -or $tagRevision -cne $record.revision -or $record.dirty) { throw 'Delivery requires a clean checkout with ReleaseTag at HEAD.' }
    }
    if ($PreviousTag -and $PreviousZip) { throw 'Use PreviousTag or PreviousZip, not both.' }
    $evidence = Join-Path ($ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($EvidenceDir)) ((Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $evidence -Force | Out-Null
    Start-Transcript -LiteralPath (Join-Path $evidence 'checks.log') | Out-Null
    $transcript = $true
    Write-Host "Source: $($record.revision); dirty=$($record.dirty); scope=$Scope"
    $env:GOTOOLCHAIN = 'local'
    $env:CGO_ENABLED = '0'
    foreach ($name in @('SWD_SETTINGS_BROWSER_EVIDENCE', 'SWD_HOSTS_BROWSER_EVIDENCE')) { [Environment]::SetEnvironmentVariable($name, $null) }
    Invoke-Checked node @('--version')
    if ($Scope -ne 'repository') { Invoke-Checked go @('version') }
    if ($Scope -in @('all', 'tools')) {
        $nodeTests = @(Get-ChildItem -LiteralPath scripts -Filter '*.test.mjs' | Where-Object { $_.Name -ne 'check-repository-artifacts.test.mjs' } | ForEach-Object { 'scripts/' + $_.Name })
        Invoke-Isolated (@('node', '--test') + $nodeTests)
        $record.passed += 'tools'
    }
    if ($Scope -in @('all', 'repository')) {
        Invoke-Isolated @('node', '--test', 'scripts/check-repository-artifacts.test.mjs')
        Invoke-Checked node @('scripts/check-repository-artifacts.mjs')
        Invoke-Isolated @('python', '-m', 'unittest', 'discover', '-s', 'scripts', '-p', 'test_*.py')
        $record.passed += 'repository'
    }
    if ($Scope -in @('all', 'product')) {
        Invoke-Checked pnpm @('--version')
        Invoke-Checked pnpm @('--dir', 'webui', 'install', '--frozen-lockfile')
        Invoke-Checked pnpm @('--dir', 'webui', 'check')
        if ($Representative) { Invoke-Isolated @('pnpm', '--dir', 'webui', 'exec', 'playwright', 'test', 'e2e/api-fixture.spec.ts', 'e2e/terms-consent.spec.ts', 'e2e/regressions.spec.ts') }
        else { Invoke-Isolated @('pnpm', '--dir', 'webui', 'test') }
        try {
            $env:SWD_SETTINGS_BROWSER_EVIDENCE = Join-Path $evidence 'settings-browser'
            $env:SWD_HOSTS_BROWSER_EVIDENCE = Join-Path $evidence 'hosts-browser'
            Invoke-Go @('test', '-tags=no_easytier', './internal/console', '-run=^(TestSettingsSeparateBrowsers|TestHostsPolicyBrowser)$', '-count=1', '-v')
        } finally {
            foreach ($name in @('SWD_SETTINGS_BROWSER_EVIDENCE', 'SWD_HOSTS_BROWSER_EVIDENCE')) { [Environment]::SetEnvironmentVariable($name, $null) }
        }
        Invoke-Go @('test', '-tags=no_easytier', './...')
        Invoke-Go @('vet', '-tags=no_easytier', './...')
        Invoke-Go @('build', '-tags=no_easytier', './...')
        $unformatted = @(& gofmt -l cmd internal)
        if ($LASTEXITCODE -ne 0 -or $unformatted.Count -ne 0) { $unformatted; throw 'Go files require formatting.' }
        $record.passed += 'product'
    }
    if ($Scope -in @('all', 'race')) {
        $env:CGO_ENABLED = '1'
        if (!$env:CC) { throw 'Set CC to the pinned Windows GCC used by check.yml before race checks.' }
        Invoke-Checked $env:CC @('--version')
        if ($Representative) {
            $tests = '^(TestAnyTLSStreamConcurrentClose|TestVRChatExitWaitsWithoutPolling|TestVRChatExitWaitsForOtherInstancesAndRestart|TestVRChatExitObservationErrorsRecover|TestHTTPSeekDuringRangeInitialization|TestBlockDemandFollowsActualWaitAndIndependentReaders|TestNetworkConnectivityFullRunsThreeRequestsTogether|TestMixedDemandKeepsDeadlineAndOpportunisticAdmission)$'
            Invoke-Go @('test', '-tags=no_easytier', '-race', './internal/blocksched', './internal/cacheproxy', './internal/console', './internal/desktop', './internal/proxyengine/mihomo', "-run=$tests", '-count=1')
        } else {
            $shards = if ($RaceShard -eq -1) { @(0..5) } else { @($RaceShard) }
            foreach ($shard in $shards) { & "$PSScriptRoot/test-race-shard.ps1" -Shard $shard -Shards 6 }
            if ($RaceShard -in @(-1, 0)) {
                $packages = @(& node scripts/go-patches.mjs list -tags=no_easytier ./... | Where-Object { $_ -notin @('still-wanna-dance/internal/cacheproxy', 'still-wanna-dance/internal/console') })
                if ($LASTEXITCODE -ne 0 -or $packages.Count -eq 0) { throw 'Could not enumerate remaining Go packages.' }
                Invoke-Go (@('test', '-tags=no_easytier', '-race') + $packages + @('-count=1', '-timeout=45m'))
            }
        }
        $record.passed += 'race'
    }
    if ($Scope -in @('all', 'delivery')) {
        $env:CGO_ENABLED = '0'
        if ($PreviousTag) {
            Invoke-Checked node @('scripts/release.mjs', 'download-previous', '--tag', $PreviousTag, '--output', 'artifacts/upgrade-baseline')
            $PreviousZip = "artifacts/upgrade-baseline/still-wanna-dance-$PreviousTag-windows-amd64-portable.zip"
        }
        $packageArgs = @{ ReleaseTag = $ReleaseTag }
        if ($PreviousZip) { $packageArgs.PreviousZip = $PreviousZip }
        & "$PSScriptRoot/package-portable.ps1" @packageArgs
        $record.passed += 'delivery'
    }
    $record.status = 'passed'
} finally {
    if ($transcript) {
        $record | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $evidence 'result.json') -Encoding utf8
        Stop-Transcript | Out-Null
        Write-Host "Preflight evidence: $evidence"
    }
    foreach ($name in $savedEnv.Keys) { [Environment]::SetEnvironmentVariable($name, $savedEnv[$name]) }
    Pop-Location
}
