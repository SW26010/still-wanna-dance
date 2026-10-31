#requires -Version 7.3
param(
    [Parameter(Mandatory = $true)][ValidateSet('create', 'list', 'extract')][string]$Action,
    [Parameter(Mandatory = $true)][string]$Archive,
    [string]$Directory
)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)

if ($Action -eq 'create') {
    $file = [IO.File]::Open($Archive, [IO.FileMode]::CreateNew)
    try {
        $gzip = [IO.Compression.GZipStream]::new($file, [IO.Compression.CompressionLevel]::Optimal, $true)
        try { [System.Formats.Tar.TarFile]::CreateFromDirectory($Directory, $gzip, $true) }
        finally { $gzip.Dispose() }
    } finally { $file.Dispose() }
} else {
    $file = [IO.File]::OpenRead($Archive)
    try {
        $header = [byte[]]::new(4)
        $null = $file.Read($header, 0, $header.Length)
        $file.Position = 0
        if ([BitConverter]::ToUInt32($header, 0) -in @(0x04034b50, 0x06054b50, 0x08074b50)) {
            if ($Action -eq 'extract') {
                [IO.Compression.ZipFile]::ExtractToDirectory($Archive, $Directory)
            } else {
                $zip = [IO.Compression.ZipArchive]::new($file, [IO.Compression.ZipArchiveMode]::Read, $true)
                try {
                    $entries = foreach ($entry in $zip.Entries) {
                        $mode = ($entry.ExternalAttributes -shr 16) -band 0xf000
                        $type = if ($mode -eq 0xa000) { 'SymbolicLink' } elseif ($mode -notin @(0, 0x8000, 0x4000)) { 'Special' } elseif ($entry.FullName.EndsWith('/')) { 'Directory' } else { 'RegularFile' }
                        @{name = $entry.FullName; type = $type}
                    }
                    ConvertTo-Json -InputObject @($entries) -Compress
                } finally { $zip.Dispose() }
            }
            return
        }
        $gzipHeader = $header[0] -eq 0x1f -and $header[1] -eq 0x8b
        $stream = if ($gzipHeader) { [IO.Compression.GZipStream]::new($file, [IO.Compression.CompressionMode]::Decompress, $true) } else { $file }
        try {
            if ($Action -eq 'extract') {
                [System.Formats.Tar.TarFile]::ExtractToDirectory($stream, $Directory, $false)
            } else {
                $reader = [System.Formats.Tar.TarReader]::new($stream, $true)
                try {
                    $entries = while ($null -ne ($entry = $reader.GetNextEntry())) {
                        if ($entry.EntryType -ne [System.Formats.Tar.TarEntryType]::GlobalExtendedAttributes) {
                            @{name = $entry.Name; type = $entry.EntryType.ToString()}
                        }
                    }
                    ConvertTo-Json -InputObject @($entries) -Compress
                } finally { $reader.Dispose() }
            }
        } finally { if ($stream -ne $file) { $stream.Dispose() } }
    } finally { $file.Dispose() }
}
