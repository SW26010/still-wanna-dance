param(
    [Parameter(Mandatory = $true)][string]$Zip,
    [string]$PreviousZip,
    [string]$EvidenceDir = (Join-Path (Split-Path -Parent $PSScriptRoot) 'artifacts/portable-smoke')
)
$ErrorActionPreference = 'Stop'
$zipPath = (Resolve-Path -LiteralPath $Zip).Path
$previousZipPath = if ($PreviousZip) { (Resolve-Path -LiteralPath $PreviousZip).Path } else { $zipPath }
# Inspection material must outlive TEMP and any disposable release checkout.
$evidencePath = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($EvidenceDir)
New-Item -ItemType Directory -Path $evidencePath -Force | Out-Null
$root = Join-Path $evidencePath ('Still Wanna Dance smoke ' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
$process = $null
$duplicate = $null
$customProcesses = @()

function Expand-Portable($archivePath, $destination) {
    $checksum = [IO.File]::ReadAllText("$archivePath.sha256").Trim()
    $hash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($checksum -cne "$hash  $([IO.Path]::GetFileName($archivePath))") { throw 'Portable ZIP checksum mismatch.' }
    $expected = @(Get-Content -LiteralPath "$PSScriptRoot/portable-files.json" -Raw | ConvertFrom-Json | ForEach-Object { "SWD/$_" } | Sort-Object)
    $archive = [IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
        $files = @()
        foreach ($entry in $archive.Entries) {
            if ($entry.FullName -cin @('SWD/', 'SWD/licenses/')) { continue }
            if ($entry.Length -eq 0) { throw "Empty or unknown ZIP entry: $($entry.FullName)" }
            $files += $entry.FullName
        }
        if ((($files | Sort-Object) -join '|') -cne ($expected -join '|')) { throw 'Unexpected Portable ZIP layout or bundled user data.' }
    } finally { $archive.Dispose() }
    Expand-Archive -LiteralPath $archivePath -DestinationPath $destination -Force
}

function Read-Build($executable) {
    try { $value = & $executable -build-info | ConvertFrom-Json }
    catch { throw "Cannot read executable build identity: $_" }
    if ($LASTEXITCODE -ne 0 -or !$value.version -or $value.revision -cnotmatch '^[a-f0-9]{40}$' -or !$value.builtAtUtc -or $value.target -cne 'windows/amd64' -or $value.toolchain -notmatch '^go\d') { throw 'Incomplete executable build identity.' }
    $source = Get-Content -LiteralPath (Join-Path (Split-Path -Parent $executable) 'licenses/SOURCE.json') -Raw | ConvertFrom-Json
    $sourceVersion = if ($source.kind -ceq 'formal') { $source.tag } else { $source.version }
    if ($value.version -cne $sourceVersion -or $value.revision -cne $source.revision -or $value.kind -cne $source.kind -or $value.kind -cnotin @('development', 'formal')) { throw 'Executable/source identity mismatch.' }
    if (($value.kind -ceq 'formal' -and $value.dirty) -or ($value.kind -ceq 'development' -and $value.dirty -ne $source.dirty)) { throw 'Executable source cleanliness mismatch.' }
    return $value
}

function Free-Port {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    return $port
}

function Wait-ConsentGate($child, $port) {
    for ($i = 0; $i -lt 40; $i++) {
        if ($child.HasExited) { throw "Console exited: $($child.ExitCode)" }
        try {
            $response = Invoke-WebRequest "http://127.0.0.1:$port/api/status" -SkipHttpErrorCheck -TimeoutSec 1
            if ([int]$response.StatusCode -eq 428) { return }
        } catch { }
        Start-Sleep -Milliseconds 250
    }
    throw 'Console did not restore its consent gate.'
}

function Data-Hashes($directory) {
    $result = @{}
    foreach ($file in Get-ChildItem -LiteralPath $directory -File -Recurse) {
        $result[[IO.Path]::GetRelativePath($directory, $file.FullName)] = (Get-FileHash -LiteralPath $file.FullName).Hash
    }
    return $result
}

function Assert-Identity($child, $port, $executable, $config) {
    $identity = Invoke-RestMethod "http://127.0.0.1:$port/api/identity" -TimeoutSec 2
    if ($identity.app -cne 'swd-console' -or $identity.protocol -ne 1 -or $identity.pid -ne $child.Id) {
        throw 'Portable identity did not identify the SWD child process.'
    }
    if ($identity.executable -ine $executable -or $identity.config -ine $config) {
        throw 'Portable identity reported the wrong installation or configuration.'
    }
    if ($identity.started -cne $child.StartTime.ToFileTimeUtc().ToString()) {
        throw 'Portable identity did not match the OS process creation time.'
    }
}
try {
    Expand-Portable $previousZipPath $root
    $exe = @(Get-Item -LiteralPath (Join-Path $root 'SWD/SWD.exe'))
    if ($exe.Count -ne 1) { throw 'Expected exactly one desktop executable.' }
    $folder = $exe[0].DirectoryName
    if ((Split-Path -Leaf $folder) -cne 'SWD') { throw 'ZIP installation directory must be stable: SWD.' }
    $shipped = @(Get-ChildItem -LiteralPath $folder | ForEach-Object Name | Sort-Object)
    if (($shipped -join '|') -cne 'licenses|SWD.exe') { throw 'Unexpected delivered files or bundled user data.' }
    $initialBuild = Read-Build $exe[0].FullName
    $initialExeHash = (Get-FileHash -LiteralPath $exe[0].FullName).Hash
    if (Test-Path -LiteralPath (Join-Path $folder 'data')) { throw 'Build diagnostic created runtime data.' }
    foreach ($name in @('LICENSE', 'LICENSE-MIT', 'LICENSING.md', 'THIRD-PARTY-NOTICES.txt', 'SOURCE.txt', 'SOURCE.json')) {
        $file = Join-Path (Join-Path $folder 'licenses') $name
        if (!(Test-Path -LiteralPath $file -PathType Leaf) -or (Get-Item -LiteralPath $file).Length -eq 0) {
            throw "Missing or empty license file: $name"
        }
    }
    $notices = Get-Content -LiteralPath (Join-Path $folder 'licenses/THIRD-PARTY-NOTICES.txt') -Raw
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
    if ($status.Content -notmatch 'id="swd-bootstrap"' -or $status.Content -notmatch '"termsGate":\{' -or $status.Content -notmatch '/assets/webui/app.js') { throw 'Fresh install did not show the consent-gated Fluent setup page.' }
    foreach ($asset in @('app.js', 'app.css')) {
        $resource = Invoke-WebRequest "$url/assets/webui/$asset" -UseBasicParsing
        if ($resource.StatusCode -ne 200 -or $resource.RawContentLength -lt 1000) { throw "Embedded Fluent resource missing: $asset" }
    }
    $gate = Invoke-WebRequest "$url/api/status" -SkipHttpErrorCheck
    if ([int]$gate.StatusCode -ne 428) { throw 'API was accessible before consent.' }
    $servedTerms = Invoke-WebRequest "$url/terms.txt" -UseBasicParsing
    # An explicit pipeline waits for a GUI-subsystem EXE and captures its UTF-8 stdout.
    $exportedTerms = (& $exe[0].FullName -show-terms | Out-String).TrimEnd().Replace("`r`n", "`n")
    if ($LASTEXITCODE -ne 0 -or $servedTerms.Content.TrimEnd().Replace("`r`n", "`n") -cne $exportedTerms) { throw 'Offline terms export and HTTP terms differ.' }
    foreach ($route in @('help', 'about')) {
        $document = Invoke-WebRequest "$url/$route"
        if ($document.Content -notmatch ('"page":"' + $route + '"')) { throw "Offline document unavailable before consent: $route" }
        if ($route -ceq 'about' -and !$document.Content.Contains('"version":"' + $initialBuild.version + '"')) { throw 'About page did not expose executable identity.' }
    }
    if (!(Test-Path -LiteralPath (Join-Path $folder 'data/config.lock'))) { throw 'Config lock missing under data.' }
    $configPath = Join-Path $folder 'data/config.json'
    Assert-Identity $process $port $exe[0].FullName $configPath
    $configBefore = [IO.File]::ReadAllText($configPath)
    $settings = $configBefore | ConvertFrom-Json
    if ($settings.schemaVersion -ne 3 -or $settings.storageDir -cne 'storage') {
        throw 'First launch did not persist a versioned, portable default configuration.'
    }
    if (Test-Path -LiteralPath (Join-Path $folder 'data/storage')) { throw 'Consent-gated startup created media storage.' }
    $generated = @(Get-ChildItem -LiteralPath (Join-Path $folder 'data') | ForEach-Object Name | Sort-Object)
    if (($generated -join '|') -cne 'config.json|config.lock|logs') { throw 'Unused feature created state or credentials.' }
    if ((Get-ChildItem -LiteralPath $folder | Measure-Object).Count -ne 3) { throw 'Startup scattered files beside SWD.exe.' }
    $page = Invoke-WebRequest $url -UseBasicParsing
    if ($page.Content -notmatch 'Still Wanna Dance') { throw 'Embedded UI missing.' }
    $logPath = Join-Path $folder 'data/logs/app.log'
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
    if ($diagnostic -notmatch '已有实例正在使用配置' -or !$diagnostic.Contains((Join-Path $folder 'data/config.json'))) {
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
    Assert-Identity $process $port $exe[0].FullName $configPath
    if ([IO.File]::ReadAllText($configPath) -cne $configBefore) { throw 'Restart rewrote persisted defaults.' }
    $records = @(Get-Content -LiteralPath $logPath | ForEach-Object { $_ | ConvertFrom-Json })
    foreach ($event in $startupEvents) {
        if (@($records | Where-Object { $_.msg -eq $event }).Count -ne 2) { throw "Expected two startup log records after restart: $event" }
    }
    # Overlay only delivered paths. Unknown user files and configuration must survive byte-for-byte.
    $process.Kill()
    $process.WaitForExit()
    [IO.File]::WriteAllText((Join-Path $folder 'data/user-note.txt'), 'Keep this user-owned file.')
    [IO.File]::WriteAllText((Join-Path $folder 'user-note.txt'), 'Keep unknown files beside the program too.')
    $before = Data-Hashes (Join-Path $folder 'data')
    Expand-Portable $zipPath $root
    $updatedBuild = Read-Build $exe[0].FullName
    if ($PreviousZip -and ($updatedBuild.version -ceq $initialBuild.version -or (Get-FileHash -LiteralPath $exe[0].FullName).Hash -ceq $initialExeHash)) { throw 'Cross-version verification requires different executable versions and bytes.' }
    if ([IO.File]::ReadAllText((Join-Path $folder 'user-note.txt')) -cne 'Keep unknown files beside the program too.') { throw 'Upgrade changed an unknown installation file.' }
    $after = Data-Hashes (Join-Path $folder 'data')
    if ($before.Count -ne $after.Count) { throw 'Upgrade added or removed user data.' }
    foreach ($key in $before.Keys) {
        if ($before[$key] -cne $after[$key]) { throw "Upgrade changed user data: $key" }
    }
    # Start the replacement at the original path before testing a whole-directory move.
    $process = Start-Process -FilePath $exe[0].FullName -ArgumentList @('-no-tray', '-no-open', '-listen', "127.0.0.1:$port") -WorkingDirectory $root -WindowStyle Hidden -PassThru
    Wait-ConsentGate $process $port
    Assert-Identity $process $port $exe[0].FullName $configPath
    if ([IO.File]::ReadAllText($configPath) -cne $configBefore) { throw 'Replacement startup rewrote configuration.' }
    $about = Invoke-WebRequest "$url/about"
    if (!$about.Content.Contains('"version":"' + $updatedBuild.version + '"')) { throw 'Replacement did not serve its own build identity.' }
    $process.Kill()
    $process.WaitForExit()

    # Both paths are generated descendants of this smoke directory, never a real installation.
    $moved = [IO.Path]::GetFullPath((Join-Path $root 'moved SWD'))
    $boundary = [IO.Path]::GetFullPath($root).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    foreach ($path in @([IO.Path]::GetFullPath($folder), $moved)) {
        if (!$path.StartsWith($boundary, [StringComparison]::OrdinalIgnoreCase)) { throw 'Move escaped the smoke directory.' }
    }
    Move-Item -LiteralPath $folder -Destination $moved
    $folder = $moved
    $exePath = Join-Path $folder 'SWD.exe'
    $process = Start-Process -FilePath $exePath -ArgumentList @('-no-tray', '-no-open', '-listen', "127.0.0.1:$port") -WorkingDirectory $root -WindowStyle Hidden -PassThru
    Wait-ConsentGate $process $port
    Assert-Identity $process $port $exePath (Join-Path $folder 'data/config.json')
    if ([IO.File]::ReadAllText((Join-Path $folder 'data/config.json')) -cne $configBefore) { throw 'Move rewrote portable configuration.' }
    if (Test-Path -LiteralPath (Join-Path $root 'data')) { throw 'Working directory received portable data.' }

    # Concurrent custom instances share a parent, but never locks, logs or defaults.
    $profiles = Join-Path $root 'profiles'
    foreach ($name in @('a.json', 'b.json')) {
        $config = Join-Path $profiles $name
        $configArgument = if ($name -eq 'a.json') { Join-Path 'profiles' $name } else { $config }
        $customPort = Free-Port
        $child = Start-Process -FilePath $exePath -ArgumentList @('-no-tray', '-no-open', '-listen', "127.0.0.1:$customPort", '-config', ('"' + $configArgument + '"')) -WorkingDirectory $root -WindowStyle Hidden -PassThru
        $customProcesses += $child
        Wait-ConsentGate $child $customPort
        Assert-Identity $child $customPort $exePath $config
        $saved = Get-Content -LiteralPath $config -Raw | ConvertFrom-Json
        if ($saved.schemaVersion -ne 3 -or $saved.storageDir -cne (Join-Path "$name.d" 'storage')) { throw 'Custom default storage escaped its instance.' }
        $records = @(Get-Content -LiteralPath (Join-Path "$config.d" 'logs/app.log') | ForEach-Object { $_ | ConvertFrom-Json })
        if (@($records | Where-Object { $_.pid -ne $child.Id }).Count -ne 0) { throw 'Custom instances shared a log.' }
        if (!(Test-Path -LiteralPath (Join-Path "$config.d" 'config.lock'))) { throw 'Custom lock missing.' }
        if (Test-Path -LiteralPath (Join-Path "$config.d" 'storage')) { throw 'Custom startup created unused storage.' }
    }
    foreach ($child in $customProcesses) { if ($child.HasExited) { throw 'Concurrent configuration was displaced.' } }

    $legacy = Join-Path $root 'legacy'
    New-Item -ItemType Directory -Path $legacy | Out-Null
    Copy-Item -LiteralPath $exePath -Destination (Join-Path $legacy 'SWD.exe')
    $marker = Join-Path $legacy 'still-wanna-dance-console.json'
    [IO.File]::WriteAllText($marker, 'old test data')
    $legacyError = Join-Path $root 'legacy.stderr.txt'
    $duplicate = Start-Process -FilePath (Join-Path $legacy 'SWD.exe') -ArgumentList @('-no-tray', '-no-open', '-listen', '127.0.0.1:0') -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardError $legacyError -PassThru
    if (!$duplicate.WaitForExit(10000) -or $duplicate.ExitCode -eq 0) { throw 'Old installation was not rejected.' }
    if ((Get-Content -LiteralPath $legacyError -Raw) -notmatch '旧测试布局') { throw 'Old installation diagnostic missing.' }
    if ([IO.File]::ReadAllText($marker) -cne 'old test data' -or (Test-Path -LiteralPath (Join-Path $legacy 'data'))) { throw 'Old installation was changed.' }
    Write-Host 'Portable smoke passed: stable ZIP paths, lazy data layout, independent launch directory, config lock, consent, legal notices, JSON logs, duplicate rejection, restart, overlay upgrade, whole-directory move, concurrent custom configurations and old-layout rejection.'
} finally {
    foreach ($child in $customProcesses) { if (!$child.HasExited) { $child.Kill(); $child.WaitForExit() } }
    if ($null -ne $duplicate -and !$duplicate.HasExited) { $duplicate.Kill(); $duplicate.WaitForExit() }
    if ($null -ne $process -and !$process.HasExited) { $process.Kill(); $process.WaitForExit() }
    # Keep the isolated extraction for inspection; never touch an existing installation.
    Write-Host "Smoke test files: $root"
}
