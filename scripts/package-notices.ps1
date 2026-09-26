param(
    [Parameter(Mandatory = $true)][string]$Executable,
    [Parameter(Mandatory = $true)][string]$Output
)
$ErrorActionPreference = 'Stop'
$source = Join-Path (Split-Path -Parent $PSScriptRoot) 'THIRD-PARTY-NOTICES.txt'
$notices = [IO.File]::ReadAllText($source)
$info = @(& go version -m $Executable)
if ($LASTEXITCODE -ne 0) { throw 'Cannot read executable dependency metadata.' }
if (@($info | Where-Object { $_ -match '^\s+path\s+stepstash/cmd/stepstash-console$' }).Count -ne 1) {
    throw 'Expected a StepStash desktop executable.'
}
foreach ($line in $info) {
    if ($line -match '^\s+=>') { throw 'Replacement modules require a license review before packaging.' }
    if ($line -match '^\s+dep\s+(\S+)\s+(\S+)') {
        $marker = "Module: $($Matches[1]) $($Matches[2])"
        if (($notices -split '\r?\n') -cnotcontains $marker) {
            throw "Missing reviewed third-party notices: $marker. Update THIRD-PARTY-NOTICES.txt."
        }
    }
}
# Use the selected toolchain, including when Go automatically switches versions.
$goRoot = & go env GOROOT
if ($LASTEXITCODE -ne 0) { throw 'Cannot locate the Go toolchain licenses.' }
$goVersion = & go env GOVERSION
if ($LASTEXITCODE -ne 0) { throw 'Cannot identify the Go toolchain.' }
if (!$info[0].EndsWith(": $goVersion")) { throw 'Executable and selected Go toolchain versions differ.' }
$builder = [Text.StringBuilder]::new($notices.TrimEnd())
[void]$builder.AppendLine().AppendLine().AppendLine("Go runtime and standard library: $goVersion")
$files = @((Get-Item -LiteralPath (Join-Path $goRoot 'LICENSE')))
# Include bundled library notices conservatively; not all are linked on every platform.
$files += @(Get-ChildItem -LiteralPath (Join-Path $goRoot 'src/vendor') -Recurse -File |
    Where-Object { $_.Name -match '^(LICENSE|LICENCE|COPYING|NOTICE|COPYRIGHT)(\.|$)' } |
    Sort-Object FullName)
foreach ($file in $files) {
    $relative = $file.FullName.Substring($goRoot.Length).TrimStart('\', '/').Replace('\', '/')
    [void]$builder.AppendLine().AppendLine("Source: Go $goVersion / $relative")
    [void]$builder.AppendLine([IO.File]::ReadAllText($file.FullName).TrimEnd())
}
[IO.File]::WriteAllText($ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Output), $builder.ToString(), [Text.UTF8Encoding]::new($false))
