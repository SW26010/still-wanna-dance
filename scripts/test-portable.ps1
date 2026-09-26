param([Parameter(Mandatory = $true)][string]$Zip)
$ErrorActionPreference = 'Stop'
$zipPath = (Resolve-Path -LiteralPath $Zip).Path
$root = Join-Path ([IO.Path]::GetTempPath()) ('StepStash smoke ' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
$process = $null
try {
    Expand-Archive -LiteralPath $zipPath -DestinationPath $root
    $exe = @(Get-ChildItem -LiteralPath $root -Recurse -Filter stepstash-console.exe)
    if ($exe.Count -ne 1) { throw 'Expected exactly one desktop executable.' }
    $folder = $exe[0].DirectoryName
    foreach ($name in @('LICENSE', 'THIRD-PARTY-NOTICES.txt')) {
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
        try { $status = Invoke-RestMethod "$url/api/status" -TimeoutSec 1; break } catch { Start-Sleep -Milliseconds 250 }
    }
    if ($null -eq $status) { throw 'Portable startup timed out.' }
    if ($status.settings.storageDir -ne (Join-Path $folder 'stepstash-data')) { throw 'Storage path is not relative to the executable.' }
    if (!(Test-Path -LiteralPath (Join-Path $folder 'stepstash-console.json.lock'))) { throw 'Config lock missing beside executable.' }
    $page = Invoke-WebRequest $url -UseBasicParsing
    if ($page.Content -notmatch 'StepStash') { throw 'Embedded UI missing.' }
    $logPath = Join-Path $folder 'logs/stepstash-console.json.log'
    $records = @(Get-Content -LiteralPath $logPath | ForEach-Object { $_ | ConvertFrom-Json })
    $startupEvents = @('application_starting', 'application_build', 'console_ready')
    foreach ($event in $startupEvents) {
        if (@($records | Where-Object { $_.msg -eq $event }).Count -ne 1) { throw "Expected one startup log record: $event" }
    }
    if ($records.Count -ne $startupEvents.Count) { throw 'Read-only status/UI requests should not generate log records.' }
    if ($process.HasExited) { throw 'Portable process did not remain alive.' }
    Write-Host 'Portable smoke passed: ZIP extraction, independent launch directory, executable-relative settings, embedded UI, JSON logs.'
} finally {
    if ($null -ne $process -and !$process.HasExited) { $process.Kill(); $process.WaitForExit() }
    # Keep the isolated extraction for inspection; never touch an existing installation.
    Write-Host "Smoke test files: $root"
}
