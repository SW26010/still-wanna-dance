param([Parameter(Mandatory=$true)][string]$TraceDirectory)
$ErrorActionPreference = 'Stop'
$traceRoot = [IO.Path]::GetFullPath($TraceDirectory)
if (-not (Test-Path -LiteralPath $traceRoot -PathType Container)) { throw 'Trace directory must already exist' }
$xperf = 'C:\Program Files (x86)\Windows Kits\10\Windows Performance Toolkit\xperf.exe'
$markerName = 'CodexCFCPUMarkers'
$markerProvider = 'E3D074A8-25FB-4D47-9427-3BC102CF5D31'
$statusPath = Join-Path $traceRoot 'trace-status.json'
$requestPath = Join-Path $traceRoot 'trace-request.json'
$kernelOwned = $false
$markerOwned = $false
$lastId = ''
$stem = ''
function Publish([string]$id, [string]$state, [string]$detail) {
    $value = [ordered]@{ id=$id; state=$state; detail=$detail }
    $temp = Join-Path $traceRoot 'trace-status.tmp'
    [IO.File]::WriteAllText($temp, ($value | ConvertTo-Json -Compress))
    Move-Item -LiteralPath $temp -Destination $statusPath -Force
}
function Invoke-Xperf([string[]]$Arguments) {
    # xperf writes successful merge diagnostics to stderr. Windows PowerShell
    # wraps them as NativeCommandError; only its exit code indicates failure.
    $ErrorActionPreference = 'Continue'
    & $xperf @Arguments 2>&1 | Out-File -LiteralPath (Join-Path $traceRoot 'trace-helper-private.log') -Append -Encoding utf8
    if ($LASTEXITCODE -ne 0) { throw "xperf exit $LASTEXITCODE" }
}
function Stop-OwnedTrace {
    if ($script:markerOwned) { Invoke-Xperf @('-stop',$markerName); $script:markerOwned=$false }
    if ($script:kernelOwned) {
        Invoke-Xperf @('-d',(Join-Path $traceRoot ($script:stem+'-kernel-merged.etl')))
        $script:kernelOwned=$false
        if (Test-Path -LiteralPath (Join-Path $traceRoot ($script:stem+'-markers.etl'))) {
            Invoke-Xperf @('-merge',(Join-Path $traceRoot ($script:stem+'-kernel-merged.etl')),(Join-Path $traceRoot ($script:stem+'-markers.etl')),(Join-Path $traceRoot ($script:stem+'.etl')))
        }
    }
}
try {
    $identity=[Security.Principal.WindowsIdentity]::GetCurrent()
    $principal=[Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Administrator token required' }
    Publish '' 'ready' 'Elevated helper; only bounded ETW start/stop requests accepted'
    $deadline=[DateTime]::UtcNow.AddMinutes(60)
    while ([DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 250
        if (-not (Test-Path -LiteralPath $requestPath)) { continue }
        try { $request=Get-Content -LiteralPath $requestPath -Raw | ConvertFrom-Json } catch { continue }
        if (-not $request.id -or $request.id -eq $lastId) { continue }
        $lastId=[string]$request.id
        switch ($request.action) {
            'start' {
                if ($kernelOwned -or $markerOwned) { throw 'This helper already owns a trace' }
                $stem=[string]$request.stem
                if ($stem -notmatch '^[a-z0-9-]{1,64}$') { throw 'Invalid trace filename stem' }
                $loggers=(& $xperf -loggers 2>&1) -join "`n"
                if ($loggers -match 'Logger Name\s+: NT Kernel Logger' -or $loggers -match ('Logger Name\s+: '+$markerName)) { throw 'Existing trace session found; refusing to modify it' }
                if (Test-Path -LiteralPath (Join-Path $traceRoot ($stem+'.etl'))) { throw 'Refusing to overwrite existing evidence' }
                Invoke-Xperf @('-on','PROC_THREAD+LOADER+CSWITCH+DISPATCHER+DPC+INTERRUPT','-BufferSize','256','-MinBuffers','64','-MaxBuffers','256','-f',(Join-Path $traceRoot ($stem+'-kernel.etl')))
                $kernelOwned=$true
                Invoke-Xperf @('-start',$markerName,'-on',($markerProvider+':0x1:5'),'-BufferSize','64','-MinBuffers','4','-MaxBuffers','16','-f',(Join-Path $traceRoot ($stem+'-markers.etl')))
                $markerOwned=$true
                Publish $lastId 'recording' $stem
            }
            'stop' {
                Stop-OwnedTrace
                Publish $lastId 'stopped' $stem
            }
            'quit' {
                Stop-OwnedTrace
                Publish $lastId 'exited' 'Owned trace sessions stopped'
                exit 0
            }
            default { throw 'Only start, stop and quit actions are accepted' }
        }
    }
    Stop-OwnedTrace
    Publish $lastId 'exited' '60-minute helper limit reached; owned trace sessions stopped'
} catch {
    $failure=$_.Exception.Message
    try { Stop-OwnedTrace } catch { $failure += '; cleanup: '+$_.Exception.Message }
    Publish $lastId 'error' $failure
    exit 1
} finally {
    if ($kernelOwned -or $markerOwned) { try { Stop-OwnedTrace } catch {} }
}
