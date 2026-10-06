param([Parameter(Mandatory = $true)][string]$Zip)
$ErrorActionPreference = 'Stop'
$zipPath = (Resolve-Path -LiteralPath $Zip).Path
$root = Join-Path ([IO.Path]::GetTempPath()) ('Still Wanna Dance smoke ' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
$process = $null
$duplicate = $null
try {
    Expand-Archive -LiteralPath $zipPath -DestinationPath $root
    $exe = @(Get-ChildItem -LiteralPath $root -Recurse -Filter still-wanna-dance-console.exe)
    if ($exe.Count -ne 1) { throw 'Expected exactly one desktop executable.' }
    $folder = $exe[0].DirectoryName
    foreach ($name in @('LICENSE', 'THIRD-PARTY-NOTICES.txt', 'TERMS.txt')) {
        $file = Join-Path $folder $name
        if (!(Test-Path -LiteralPath $file -PathType Leaf) -or (Get-Item -LiteralPath $file).Length -eq 0) {
            throw "Missing or empty license file: $name"
        }
    }
    $notices = Get-Content -LiteralPath (Join-Path $folder 'THIRD-PARTY-NOTICES.txt') -Raw
    $info = @(& go version -m $exe[0].FullName)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect packaged dependencies.' }
    foreach ($line in $info) {
        if ($line -match '^\s+dep\s+(\S+)\s+(\S+)') {
            if (($notices -split '\r?\n') -cnotcontains "Module: $($Matches[1]) $($Matches[2])") {
                throw "Packaged dependency notice missing: $line"
            }
        }
    }
    if ($notices -notmatch 'Go runtime and standard library: go\d') { throw 'Go runtime notice missing.' }
    # Reserve a candidate port, then verify the child stayed alive to avoid accepting another process.
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    $process = Start-Process -FilePath $exe[0].FullName -ArgumentList @('-no-tray', '-no-open', '-listen', "127.0.0.1:$port") -WorkingDirectory $root -WindowStyle Hidden -PassThru
    $url = "http://127.0.0.1:$port"
    $status = $null
    for ($i = 0; $i -lt 40; $i++) {
        if ($process.HasExited) { throw "Portable process exited: $($process.ExitCode)" }
        try { $status = Invoke-WebRequest "$url/" -UseBasicParsing -TimeoutSec 1; break } catch { Start-Sleep -Milliseconds 250 }
    }
    if ($null -eq $status) { throw 'Portable startup timed out.' }
    if ($status.Content -notmatch 'id="consent"') { throw 'Fresh install did not require consent.' }
    $gate = Invoke-WebRequest "$url/api/status" -SkipHttpErrorCheck
    if ([int]$gate.StatusCode -ne 428) { throw 'API was accessible before consent.' }
    $servedTerms = Invoke-WebRequest "$url/terms.txt" -UseBasicParsing
    if ($servedTerms.Content -cne [IO.File]::ReadAllText((Join-Path $folder 'TERMS.txt'))) { throw 'Packaged and embedded terms differ.' }
    if (!(Test-Path -LiteralPath (Join-Path $folder 'still-wanna-dance-console.json.lock'))) { throw 'Config lock missing beside executable.' }
    $page = Invoke-WebRequest $url -UseBasicParsing
    if ($page.Content -notmatch 'Still Wanna Dance') { throw 'Embedded UI missing.' }
    $logPath = Join-Path $folder 'logs/still-wanna-dance-console.json.log'
    $records = @(Get-Content -LiteralPath $logPath | ForEach-Object { $_ | ConvertFrom-Json })
    $startupEvents = @('application_starting', 'application_build', 'console_ready')
    foreach ($event in $startupEvents) {
        if (@($records | Where-Object { $_.msg -eq $event }).Count -ne 1) { throw "Expected one startup log record: $event" }
    }
    if ($records.Count -ne $startupEvents.Count) { throw 'Read-only status/UI requests should not generate log records.' }
    if ($process.HasExited) { throw 'Portable process did not remain alive.' }
    # Use a different port so rejection proves config ownership, not a bind conflict.
    $duplicateError = Join-Path $root 'duplicate.stderr.txt'
    $duplicate = Start-Process -FilePath $exe[0].FullName -ArgumentList @('-no-tray', '-no-open', '-listen', '127.0.0.1:0') -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardError $duplicateError -PassThru
    if (!$duplicate.WaitForExit(10000)) { throw 'Duplicate process did not reject the occupied config.' }
    if ($duplicate.ExitCode -eq 0) { throw 'Duplicate process unexpectedly succeeded.' }
    $diagnostic = Get-Content -LiteralPath $duplicateError -Raw
    if ($diagnostic -notmatch '已有实例正在使用配置' -or !$diagnostic.Contains((Join-Path $folder 'still-wanna-dance-console.json'))) {
        throw 'Duplicate process failed without identifying the occupied config.'
    }
    if ($process.HasExited -or (Invoke-WebRequest "$url/" -UseBasicParsing).StatusCode -ne 200) { throw 'Duplicate launch disrupted the owner.' }

    # Simulate abrupt process termination, then reuse the exact directory and port.
    # This checks startup/lock recovery, not interrupted download or power-loss durability.
    $process.Kill()
    $process.WaitForExit()
    $process = Start-Process -FilePath $exe[0].FullName -ArgumentList @('-no-tray', '-no-open', '-listen', "127.0.0.1:$port") -WorkingDirectory $root -WindowStyle Hidden -PassThru
    $restarted = $false
    for ($i = 0; $i -lt 40; $i++) {
        if ($process.HasExited) { throw "Restart exited: $($process.ExitCode)" }
        try {
            $gate = Invoke-WebRequest "$url/api/status" -SkipHttpErrorCheck -TimeoutSec 1
            if ([int]$gate.StatusCode -eq 428) { $restarted = $true; break }
        } catch { }
        Start-Sleep -Milliseconds 250
    }
    if (!$restarted) { throw 'Restart did not restore the consent-gated console.' }
    $records = @(Get-Content -LiteralPath $logPath | ForEach-Object { $_ | ConvertFrom-Json })
    foreach ($event in $startupEvents) {
        if (@($records | Where-Object { $_.msg -eq $event }).Count -ne 2) { throw "Expected two startup log records after restart: $event" }
    }
    Write-Host 'Portable smoke passed: ZIP extraction, independent launch directory, config lock, consent gate, matching terms, embedded UI, JSON logs, duplicate rejection, abrupt-stop restart.'
} finally {
    if ($null -ne $duplicate -and !$duplicate.HasExited) { $duplicate.Kill(); $duplicate.WaitForExit() }
    if ($null -ne $process -and !$process.HasExited) { $process.Kill(); $process.WaitForExit() }
    # Keep the isolated extraction for inspection; never touch an existing installation.
    Write-Host "Smoke test files: $root"
}
