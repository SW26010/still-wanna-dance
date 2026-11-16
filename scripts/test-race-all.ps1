param([Parameter(Mandatory = $true)][string]$EvidenceDirectory)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$jobs = @()
try {
    foreach ($shard in 0..5) {
        $directory = Join-Path $EvidenceDirectory "race-shard-$shard"
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
        $jobs += Start-Job -WorkingDirectory $root -ArgumentList $PSScriptRoot, $shard, $directory -ScriptBlock {
            param($Scripts, $Shard, $Directory)
            $ErrorActionPreference = 'Stop'
            $result = [ordered]@{ shard = $Shard; status = 'failed' }
            try {
                & "$Scripts/test-race-shard.ps1" -Shard $Shard -Shards 6 *> "$Directory/checks.log"
                $result.status = 'passed'
            } catch {
                $_ | Out-String | Add-Content -LiteralPath "$Directory/checks.log"
                throw
            } finally {
                $result | ConvertTo-Json | Set-Content -LiteralPath "$Directory/result.json" -Encoding utf8
            }
        }
        Write-Host "Started race shard $shard/6; evidence: $directory"
    }
    $pending = @($jobs)
    while ($pending.Count) {
        $finished = Wait-Job -Job $pending -Any
        Receive-Job -Job $finished -ErrorAction Continue
        Write-Host "Race shard $([array]::IndexOf($jobs, $finished))/6: $($finished.State)"
        $pending = @($pending | Where-Object Id -ne $finished.Id)
    }
    if (@($jobs | Where-Object State -ne 'Completed').Count) { throw 'Full race checks failed; inspect the individual shard logs and results.' }
} finally {
    $jobs | Where-Object State -in @('Running', 'NotStarted') | Stop-Job
    $jobs | Remove-Job
}
