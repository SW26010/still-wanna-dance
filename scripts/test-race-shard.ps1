param(
    [Parameter(Mandatory = $true)][int]$Shard,
    [int]$Shards = 6
)
$ErrorActionPreference = 'Stop'
if ($Shards -lt 1 -or $Shard -lt 0 -or $Shard -ge $Shards) { throw 'Invalid race shard.' }
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    foreach ($package in @('./internal/cacheproxy', './internal/console')) {
        # Ask the Go test binary for its top-level tests, including examples and
        # fuzz seed tests. The modulo assignment covers each name exactly once.
        $listing = @(& "$PSScriptRoot/with-test-temp.ps1" node "$PSScriptRoot/go-patches.mjs" test -race -tags=no_easytier $package -list '^(Test|Example|Fuzz)')
        if ($LASTEXITCODE -ne 0) { throw "Cannot list race tests for $package." }
        $names = @($listing | Where-Object { $_ -cmatch '^(Test|Example|Fuzz)[\p{L}\p{Nd}_]*$' })
        $unique = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        foreach ($name in $names) { if (!$unique.Add($name)) { throw "Duplicate race test name in $package`: $name" } }
        if ($names.Count -eq 0) { throw "Empty race test listing for $package." }
        $selected = @(for ($i = $Shard; $i -lt $names.Count; $i += $Shards) { $names[$i] })
        if ($selected.Count -eq 0) { continue }
        $pattern = '^(' + ($selected -join '|') + ')$'
        if ($pattern.Length -gt 30000) { throw 'Race shard pattern is too long for Windows; increase the number of shards.' }
        Write-Host "$package race shard $Shard/$Shards`: $($selected.Count) of $($names.Count) top-level tests"
        & "$PSScriptRoot/with-test-temp.ps1" node "$PSScriptRoot/go-patches.mjs" test -race -tags=no_easytier $package "-run=$pattern" -count=1 -timeout=45m
        if ($LASTEXITCODE -ne 0) { throw "Race tests failed for $package shard $Shard." }
    }
} finally { Pop-Location }
