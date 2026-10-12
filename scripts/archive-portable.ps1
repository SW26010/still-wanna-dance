param(
    [Parameter(Mandatory = $true)][string]$Folder,
    [Parameter(Mandatory = $true)][string]$Zip
)
$ErrorActionPreference = 'Stop'
Compress-Archive -LiteralPath $Folder -DestinationPath $Zip
