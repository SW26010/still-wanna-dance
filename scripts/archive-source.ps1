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
        $gzipHeader = $file.ReadByte() -eq 0x1f -and $file.ReadByte() -eq 0x8b
        $file.Position = 0
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
