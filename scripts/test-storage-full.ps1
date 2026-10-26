param(
    [Parameter(Mandatory = $true)][string]$TestBinary,
    [Parameter(Mandatory = $true)][string]$EvidenceDirectory
)

# Run an already built, targeted Go test on a NEW bounded NTFS image. Windows
# requires an administrator token to attach/partition a VHD; building the test
# and all ordinary validation remain unelevated. Never accept a disk number or
# existing image from the caller, and never format an enumerated physical disk.
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$artifacts = [IO.Path]::GetFullPath((Join-Path $repo 'artifacts'))
if (-not [IO.Path]::IsPathRooted($TestBinary)) { $TestBinary = Join-Path $repo $TestBinary }
if (-not [IO.Path]::IsPathRooted($EvidenceDirectory)) { $EvidenceDirectory = Join-Path $repo $EvidenceDirectory }
$binary = [IO.Path]::GetFullPath($TestBinary)
$evidence = [IO.Path]::GetFullPath($EvidenceDirectory)
foreach ($path in @($binary, $evidence)) {
    if (-not $path.StartsWith($artifacts + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Test binary and evidence must be inside this checkout''s artifacts directory.'
    }
}
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw 'Build the test binary first.' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Attaching the disposable VHD requires an administrator PowerShell token.'
}

$run = Join-Path $evidence ('bounded-volume-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $run | Out-Null
$image = Join-Path $run 'disposable-128MiB.vhdx'
$size = 128MB
$previousVolume = $env:SWD_FULL_TEST_VOLUME
$result = [ordered]@{
    image = $image
    sizeBytes = $size
    testBinary = $binary
    testSHA256 = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash
    testExitCode = $null
    dismounted = $false
    imageRemoved = $false
    error = $null
}
$exitCode = 1
Start-Transcript -LiteralPath (Join-Path $run 'harness.txt') | Out-Null
try {
    if (Test-Path -LiteralPath $image) { throw 'Refusing to reuse an image.' }
    $hostVolume = Get-Volume -FilePath $run
    if (@($hostVolume).Count -ne 1 -or $hostVolume.SizeRemaining -lt ($size + 512MB)) {
        throw 'The image host must retain at least 512 MiB free after allocating the bounded VHD.'
    }
    $result.hostFreeBytesBefore = $hostVolume.SizeRemaining
    New-VHD -Path $image -SizeBytes $size -Fixed | Out-Null
    Mount-VHD -Path $image -NoDriveLetter | Out-Null
    $vhd = Get-VHD -Path $image
    if (-not $vhd.Attached -or $vhd.Path -ne $image -or $vhd.Size -ne $size) {
        throw 'Mounted VHD identity or capacity did not match the newly created image.'
    }
    $disk = Get-Disk -Number $vhd.DiskNumber
    if ($disk.IsBoot -or $disk.IsSystem -or $disk.Size -ne $size -or $disk.PartitionStyle -ne 'RAW') {
        throw 'Refusing to initialize anything except the new, empty 128 MiB virtual disk.'
    }
    $diskNumber = $disk.Number
    $diskIdentity = $disk.UniqueId
    Initialize-Disk -InputObject $disk -PartitionStyle MBR | Out-Null
    $partition = New-Partition -DiskNumber $diskNumber -UseMaximumSize -AssignDriveLetter
    $vhd = Get-VHD -Path $image
    $disk = Get-Disk -Number $diskNumber
    if (-not $vhd.Attached -or $vhd.DiskNumber -ne $diskNumber -or $vhd.Path -ne $image -or
        $disk.UniqueId -ne $diskIdentity -or $disk.IsBoot -or $disk.IsSystem -or
        $disk.Size -ne $size -or $partition.DiskNumber -ne $diskNumber) {
        throw 'Virtual disk identity changed before formatting; refusing to continue.'
    }
    $volume = Format-Volume -Partition $partition -FileSystem NTFS -NewFileSystemLabel 'SWD-FULL' -Force -Confirm:$false
    if (-not $volume.DriveLetter -or $volume.Size -gt $size -or $volume.FileSystem -ne 'NTFS') {
        throw 'Unexpected disposable volume.'
    }
    $root = '{0}:\' -f $volume.DriveLetter
    [IO.File]::WriteAllText((Join-Path $root '.swd-disk-full-test'), "bounded-ntfs-v1`n", [Text.UTF8Encoding]::new($false))
    $result.diskNumber = $diskNumber
    $result.volumeRoot = $root
    $result.volumeBytes = $volume.Size
    $env:SWD_FULL_TEST_VOLUME = $root
    Push-Location (Join-Path $repo 'internal/cacheproxy')
    try {
        & (Join-Path $PSScriptRoot 'with-test-temp.ps1') $binary '-test.run=^TestBoundedVolumeExhaustion$' '-test.v' '-test.timeout=3m' 2>&1 |
            Tee-Object -FilePath (Join-Path $run 'test.txt')
        $exitCode = $LASTEXITCODE
        $result.testExitCode = $exitCode
    } finally {
        Pop-Location
    }
} catch {
    $result.error = $_.ToString()
    Write-Output $result.error
    $exitCode = 1
} finally {
    $env:SWD_FULL_TEST_VOLUME = $previousVolume
    try {
        # Dismount by the exact file this invocation created, never by an
        # unverified disk number. Retain the image if cleanup cannot be proved.
        if ((Test-Path -LiteralPath $image) -and (Get-VHD -Path $image).Attached) {
            Dismount-VHD -Path $image
            if ((Get-VHD -Path $image).Attached) { throw 'Disposable VHD is still attached.' }
            $result.dismounted = $true
        }
        $resolvedImage = [IO.Path]::GetFullPath($image)
        if ([IO.Path]::GetDirectoryName($resolvedImage) -ne $run -or
            [IO.Path]::GetFileName($resolvedImage) -ne 'disposable-128MiB.vhdx') {
            throw 'Refusing image cleanup outside this invocation''s directory.'
        }
        if (Test-Path -LiteralPath $resolvedImage) {
            Remove-Item -LiteralPath $resolvedImage
            $result.imageRemoved = $true
        }
    } catch {
        $result.cleanupError = $_.ToString()
        Write-Output $result.cleanupError
        $exitCode = 1
    }
    $result | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $run 'result.json') -Encoding utf8
    Stop-Transcript | Out-Null
}
exit $exitCode
