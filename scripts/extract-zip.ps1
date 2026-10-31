param(
    [Parameter(Mandatory = $true)][string]$Archive,
    [Parameter(Mandatory = $true)][string]$Destination
)
$ErrorActionPreference = 'Stop'
[IO.Compression.ZipFile]::ExtractToDirectory($Archive, $Destination)
