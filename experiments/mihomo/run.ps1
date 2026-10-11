param([string]$OutputDirectory = '../../artifacts/mihomo-embedding', [string]$Hold = '6s', [switch]$ExerciseAfterClose)
$ErrorActionPreference = 'Stop'
$runDir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $runDir | Out-Null
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
function Check-Exit([string]$Step) { if ($LASTEXITCODE -ne 0) { throw "$Step failed ($LASTEXITCODE)" } }
& go build -o (Join-Path $runDir 'baseline.exe') .
Check-Exit 'baseline build'
& go build -o (Join-Path $runDir 'fixture.exe') ./cmd/fixture
Check-Exit 'fixture build'
& go build -o (Join-Path $runDir 'probe.exe') ./cmd/probe
Check-Exit 'client build'
& go version -m (Join-Path $runDir 'probe.exe') | Set-Content (Join-Path $runDir 'build-info.txt')

function Read-SystemState {
    [ordered]@{
        HostsSHA256 = (Get-FileHash -LiteralPath "$env:WINDIR\System32\drivers\etc\hosts").Hash
        UserProxy = (Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' | Select-Object ProxyEnable,ProxyServer,AutoConfigURL)
        WinHTTPProxy = (@(& netsh winhttp show proxy) -join "`n")
        Adapters = @(Get-NetAdapter -IncludeHidden | Sort-Object InterfaceGuid | Select-Object InterfaceGuid,Name,InterfaceDescription)
        Routes = @(Get-NetRoute | Sort-Object InterfaceIndex,DestinationPrefix,NextHop | Select-Object InterfaceIndex,DestinationPrefix,NextHop,RouteMetric)
    }
}
$before = Read-SystemState
$before | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $runDir 'system-before.json')
$manifest = Join-Path $runDir ('fixture-' + [guid]::NewGuid().ToString('N') + '.json')
$fixtureProcess = $null
$probeProcess = $null
try {
    & (Join-Path $runDir 'baseline.exe') | Set-Content (Join-Path $runDir 'baseline.json')
    Check-Exit 'baseline startup'
    $fixtureProcess = Start-Process -FilePath (Join-Path $runDir 'fixture.exe') -ArgumentList @(('"' + $manifest + '"')) -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runDir 'fixture.stdout.log') -RedirectStandardError (Join-Path $runDir 'fixture.stderr.log')
    $readyDeadline = [DateTime]::UtcNow.AddSeconds(15)
    while (!(Test-Path -LiteralPath $manifest)) {
        if ($fixtureProcess.HasExited) { throw 'Fixture exited before readiness' }
        if ([DateTime]::UtcNow -gt $readyDeadline) { throw 'Fixture readiness timeout' }
        Start-Sleep -Milliseconds 100
    }
    $result = Join-Path $runDir 'results.json'
    $probeArgs = @('-fixture', ('"' + $manifest + '"'), '-output', ('"' + $result + '"'), '-hold', $Hold)
    if ($ExerciseAfterClose) { $probeArgs += '-exercise-after-close' }
    $probeProcess = Start-Process -FilePath (Join-Path $runDir 'probe.exe') -ArgumentList $probeArgs -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runDir 'probe.stdout.log') -RedirectStandardError (Join-Path $runDir 'probe.stderr.log')
    $samples = [Collections.Generic.List[object]]::new()
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    while (!$probeProcess.HasExited) {
        if ([DateTime]::UtcNow -gt $deadline) { throw 'Probe exceeded bounded run time' }
        $sample = [ordered]@{
            Time = [DateTime]::UtcNow.ToString('o')
            TCPListeners = @(Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object OwningProcess -eq $probeProcess.Id | Select-Object LocalAddress,LocalPort)
            UDPSockets = @(Get-NetUDPEndpoint -ErrorAction SilentlyContinue | Where-Object OwningProcess -eq $probeProcess.Id | Select-Object LocalAddress,LocalPort)
        }
        $probeProcess.Refresh()
        $sample.ProcessAlive = !$probeProcess.HasExited
        $samples.Add($sample)
        Start-Sleep -Milliseconds 100
    }
    $samples | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $runDir 'client-sockets.json')
    if ($probeProcess.ExitCode -ne 0) { throw "Probe exited $($probeProcess.ExitCode); inspect stderr" }
    Get-Content -LiteralPath $result | ConvertFrom-Json | Select-Object -ExpandProperty checks | Format-Table name,passed,milliseconds -AutoSize
} finally {
    foreach ($ownedProcess in @($probeProcess,$fixtureProcess)) { if ($null -ne $ownedProcess -and !$ownedProcess.HasExited) { Stop-Process -Id $ownedProcess.Id } }
    $after = Read-SystemState
    $after | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $runDir 'system-after.json')
    [ordered]@{ Unchanged = (($before | ConvertTo-Json -Depth 8 -Compress) -ceq ($after | ConvertTo-Json -Depth 8 -Compress)); Note = 'Global state comparison; unrelated concurrent changes may require inspection.' } | ConvertTo-Json | Set-Content (Join-Path $runDir 'side-effects.json')
}
$checks = (Get-Content -LiteralPath $result | ConvertFrom-Json).checks
# Raw adapter semantics are observations. The corresponding guard tests are gates.
$required = @($checks | Where-Object name -NotMatch '^[^/]+/(pre-canceled-dial|close-rejects-new-business)$')
$failed = @($required | Where-Object { !$_.passed })
$liveSamples = @($samples | Where-Object ProcessAlive)
$lastLive = @($liveSamples | Select-Object -Last 3)
$noListeners = @($liveSamples | Where-Object { $_.TCPListeners.Count -gt 0 }).Count -eq 0
$udpDrained = $lastLive.Count -eq 3 -and @($lastLive | Where-Object { $_.UDPSockets.Count -gt 0 }).Count -eq 0
$unchanged = (Get-Content (Join-Path $runDir 'side-effects.json') | ConvertFrom-Json).Unchanged
$summary = [ordered]@{
    Mode = $(if ($ExerciseAfterClose) { 'raw-post-close-observation' } else { 'controlled-lifecycle' })
    RequiredChecks = $required.Count
    FailedChecks = @($failed | ForEach-Object { $_.name })
    NoTCPListenersObserved = $noListeners
    UDPDrainedBeforeExit = $udpDrained
    SystemStateUnchanged = $unchanged
    Passed = ($failed.Count -eq 0 -and $noListeners -and $unchanged -and ($udpDrained -or $ExerciseAfterClose))
}
$summary | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $runDir 'summary.json')
$summary | ConvertTo-Json -Depth 4
if (!$summary.Passed) { throw 'Embedding experiment gate failed; inspect summary.json and results.json' }
