# No named parameters: all flags, including -e and -Command, belong to the child.
$Command, $commandArgs = $args
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
if (-not $Command) { throw 'Usage: with-test-temp.ps1 <command> [arguments...]' }

# MXC's default TEMP can create files but reject rename (microsoft/mxc#1204).
# Keep the workaround in the tool process, shared by Go, Node and browsers.
$tempRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../artifacts/tmp'))
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
$tempDir = Join-Path $tempRoot ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempDir | Out-Null
$previous = @{}
foreach ($key in @('TEMP', 'TMP', 'GOTMPDIR')) {
    $previous[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
try {
    foreach ($key in $previous.Keys) {
        [Environment]::SetEnvironmentVariable($key, $tempDir, 'Process')
    }
    $global:LASTEXITCODE = 0
    & $Command @commandArgs
    $commandExitCode = $global:LASTEXITCODE
} finally {
    foreach ($key in $previous.Keys) {
        if ($null -eq $previous[$key]) {
            [Environment]::SetEnvironmentVariable($key, [NullString]::Value, 'Process')
        } else {
            [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process')
        }
    }
    # Delete only this invocation's directory, never the shared parent.
    if ([IO.Path]::GetDirectoryName([IO.Path]::GetFullPath($tempDir)) -ne $tempRoot) {
        throw "Refusing to remove a temporary directory outside $tempRoot."
    }
    try {
        Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction Stop
    } catch {
        Write-Warning "Temporary directory retained at ${tempDir}: $_"
    }
}
exit $commandExitCode
