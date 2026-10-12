param(
    [Parameter(Mandatory = $true)][string]$Url,
    [Parameter(Mandatory = $true)][string]$Output
)
$ErrorActionPreference = 'Stop'
if (([uri]$Url).Scheme -cne 'https' -or (Test-Path -LiteralPath $Output)) { throw 'Expected HTTPS source and a new output file.' }
Invoke-WebRequest -Uri $Url -OutFile $Output -TimeoutSec 180
