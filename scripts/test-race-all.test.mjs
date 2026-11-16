import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';
import {withTemp} from './release-source.mjs';

const scripts=path.dirname(fileURLToPath(import.meta.url));
function raceAll(failedShard=-1) {
  return withTemp(root=>{
    const directory=path.join(root,'scripts'),evidence=path.join(root,'evidence');
    fs.mkdirSync(directory);fs.mkdirSync(path.join(root,'barrier'));
    fs.copyFileSync(path.join(scripts,'test-race-all.ps1'),path.join(directory,'test-race-all.ps1'));
    fs.writeFileSync(path.join(directory,'test-race-shard.ps1'),`
param($Shard, $Shards)
$ErrorActionPreference = 'Stop'
if ($Shards -ne 6 -or $env:CI -cne 'fixture-ci' -or $env:CGO_ENABLED -cne '1' -or $env:CC -cne 'fixture-cc') { throw 'Worker lost its configuration.' }
$barrier = Join-Path $PSScriptRoot '../barrier'
Set-Content -LiteralPath "$barrier/started-$Shard" -Value $Shard
$deadline = [DateTime]::UtcNow.AddSeconds(30)
while (@(Get-ChildItem -LiteralPath $barrier -Filter 'started-*').Count -ne 6) {
    if ([DateTime]::UtcNow -gt $deadline) { throw 'All six shards must start before any shard completes.' }
    Start-Sleep -Milliseconds 20
}
Write-Output "Executed shard $Shard"
if ($Shard -eq ${failedShard}) {
    Set-Content -LiteralPath "$barrier/failed" -Value $Shard
    throw 'fixture shard failure'
}
if (${failedShard} -ge 0) {
    while (!(Test-Path -LiteralPath "$barrier/failed")) {
        if ([DateTime]::UtcNow -gt $deadline) { throw 'Failed shard did not run.' }
        Start-Sleep -Milliseconds 20
    }
}
Set-Content -LiteralPath "$barrier/completed-$Shard" -Value $Shard
`);
    const child=spawnSync('pwsh',['-NoProfile','-File',path.join(directory,'test-race-all.ps1'),'-EvidenceDirectory',evidence],{
      encoding:'utf8',windowsHide:true,env:{...process.env,CI:'fixture-ci',CGO_ENABLED:'1',CC:'fixture-cc'},
    });
    const results=Array.from({length:6},(_,shard)=>{
      const worker=path.join(evidence,`race-shard-${shard}`);
      const result=JSON.parse(fs.readFileSync(path.join(worker,'result.json'),'utf8').replace(/^\uFEFF/,''));
      assert.equal(result.shard,shard);
      const log=fs.readFileSync(path.join(worker,'checks.log'),'utf8');
      assert.match(log,new RegExp(`Executed shard ${shard}`));
      if(shard===failedShard)assert.match(log,/fixture shard failure/);
      return result;
    });
    return {exitCode:child.status,output:child.stdout+child.stderr,results,completed:fs.readdirSync(path.join(root,'barrier')).filter(name=>name.startsWith('completed-')).length};
  });
}

test('full race starts all six shards together and retains independent logs and results',()=>{
  const result=raceAll();
  assert.equal(result.exitCode,0,result.output);
  assert.equal(result.completed,6);
  assert.ok(result.results.every(worker=>worker.status==='passed'));
});

test('one failed shard fails the aggregate after the other five finish',()=>{
  const result=raceAll(2);
  assert.equal(result.exitCode,1,result.output);
  assert.equal(result.completed,5);
  assert.deepEqual(result.results.map(worker=>worker.status),['passed','passed','failed','passed','passed','passed']);
});
