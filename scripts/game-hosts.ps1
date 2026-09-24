param(
    [Parameter(Mandatory=$true)][ValidateSet('Enable','Disable')][string]$Mode,
    [Parameter(Mandatory=$true)][string]$Lab
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$testRoot = [IO.Path]::GetFullPath((Join-Path $repo 'test-runs')) + [IO.Path]::DirectorySeparatorChar
$labPath = [IO.Path]::GetFullPath($Lab)
if (-not $labPath.StartsWith($testRoot, [StringComparison]::OrdinalIgnoreCase) -or -not (Test-Path -LiteralPath $labPath -PathType Container)) {
    throw 'Lab must be an existing directory inside this repository test-runs.'
}
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $child = Start-Process powershell.exe -Verb RunAs -WindowStyle Hidden -Wait -PassThru -ArgumentList @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', ('"' + $PSCommandPath + '"'), '-Mode', $Mode, '-Lab', ('"' + $labPath + '"'))
    if ($child.ExitCode -ne 0) { throw "Elevated hosts operation failed: $($child.ExitCode)" }
    exit
}
try {
    $hostsPath = Join-Path $env:SystemRoot 'System32/drivers/etc/hosts'
    $marker = '# StepStash-MVP-acceptance'
    $domains = @('play.udon.dance', 'nya.xin.moe')
    $encoding = [Text.Encoding]::GetEncoding(28591)
    $bytes = [IO.File]::ReadAllBytes($hostsPath)
    $text = $encoding.GetString($bytes)
    $backup = Join-Path $labPath 'hosts.before-test'
    if ($Mode -eq 'Enable') {
        if ($text.Contains($marker)) { throw 'An acceptance hosts mapping already exists; disable its session first.' }
        if ($text -match '(?m)^[^#\r\n]*\b(play\.udon\.dance|nya\.xin\.moe|api\.udon\.dance)\b') { throw 'Existing WannaDance domain mappings need inspection.' }
        $request = [Net.HttpWebRequest]::Create('http://127.0.0.1/files/2403/1343-660524b4eb86f.mp4?e=28711962048bed664c98f27e1d9d5842&s=32867177')
        $request.Host = 'play.udon.dance'
        $request.Proxy = $null
        $request.Timeout = 5000
        $request.AddRange(0,15)
        $response = $request.GetResponse()
        try {
            if ([int]$response.StatusCode -ne 206 -or $response.Headers['X-StepStash-Cache'] -ne 'HIT') { throw 'StepStash warm-cache readiness check failed.' }
        } finally { $response.Dispose() }
        if (Test-Path -LiteralPath $backup) { throw 'Backup already exists; use a new session directory.' }
        [IO.File]::WriteAllBytes($backup, $bytes)
        $addition = ($domains | ForEach-Object { "`r`n127.0.0.1 $_ $marker`r`n" }) -join ''
        [IO.File]::WriteAllBytes($hostsPath, [byte[]]($bytes + [Text.Encoding]::ASCII.GetBytes($addition)))
    } else {
        if (-not (Test-Path -LiteralPath $backup)) { throw 'Session backup missing; refusing blind cleanup.' }
        $updated = $text
        foreach ($domain in $domains) { $updated = $updated.Replace("`r`n127.0.0.1 $domain $marker`r`n", '') }
        if ($updated.Contains($marker)) { throw 'Marked entries changed; inspect before cleanup.' }
        if ($updated -ne $text) { [IO.File]::WriteAllBytes($hostsPath, $encoding.GetBytes($updated)) }
    }
    & ipconfig.exe /flushdns | Out-Null
    [IO.File]::WriteAllText((Join-Path $labPath 'hosts-action.json'), (@{
        mode=$Mode; time=(Get-Date).ToString('o');
        currentSHA256=(Get-FileHash -LiteralPath $hostsPath -Algorithm SHA256).Hash;
        backupSHA256=(Get-FileHash -LiteralPath $backup -Algorithm SHA256).Hash
    } | ConvertTo-Json))
} catch {
    [IO.File]::WriteAllText((Join-Path $labPath 'hosts-error.txt'), $_.ToString())
    throw
}
