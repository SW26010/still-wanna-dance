import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { withTemp, run, sha256 } from './release-source.mjs';

const scripts = path.dirname(fileURLToPath(import.meta.url));
const quote = value => "'" + value.replaceAll("'", "''") + "'";

test('Portable log reads support legacy upgrades and discover new segments on every read', {skip:process.platform !== 'win32'}, () => withTemp(temp => {
  const log = name => JSON.stringify({msg:name})+'\n';
  fs.writeFileSync(path.join(temp,'app.log'),log('legacy'));
  fs.writeFileSync(path.join(temp,'app.log.1'),log('older'));
  fs.writeFileSync(path.join(temp,'20261103T000000Z.log'),log('timestamp'));
  for (const name of ['app.log.4','notes.log','2026-11-03T00-00-00.000000000Z.log','20261103T000000Z-0.log']) fs.writeFileSync(path.join(temp,name),'unknown files must not be parsed');
  const result = run('pwsh',['-NoProfile','-NonInteractive','-Command',`
    $ErrorActionPreference = 'Stop'
    $parseErrors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile(${quote(path.join(scripts,'test-portable.ps1'))}, [ref]$null, [ref]$parseErrors)
    if ($parseErrors.Count) { throw 'Portable script did not parse.' }
    $definition = $ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -ceq 'Read-ApplicationLogs' }, $false)
    if (!$definition) { throw 'Portable log reader is missing.' }
    Invoke-Expression $definition.Extent.Text
    $directory = ${quote(temp)}
    $legacy = @(Read-ApplicationLogs $directory)
    try {
      Read-ApplicationLogs $directory -RequireCurrent | Out-Null
      throw 'Current build accepted only legacy logs.'
    } catch {
      if ($_.Exception.Message -notlike 'Expected date-named*') { throw }
    }
    [IO.File]::WriteAllText((Join-Path $directory '20261103.log'), ${quote('2026-11-03T12:00:00.1234+08:00 [INFO] "程序构建信息" event_name=current pid=42 version=1.0 path="with event_name=fake pid=99 version=fake"\n')})
    $first = @(Read-ApplicationLogs $directory -RequireCurrent)
    [IO.File]::WriteAllText((Join-Path $directory '20261103-1.log'), ${quote('2026-11-03T12:00:00.1234+08:00 [INFO] "不同的摘要 event_name=fake" event_name=newer pid=43 version="v1.1"\n')})
    $second = @(Read-ApplicationLogs $directory -RequireCurrent)
    @{legacy=@($legacy.event_name | Sort-Object);first=$first.Count;second=@($second.event_name | Sort-Object);current=@($first | Where-Object event_name -ceq 'current')[0];newer=@($second | Where-Object event_name -ceq 'newer')[0]} | ConvertTo-Json -Compress
  `]);
  const summary = JSON.parse(result);
  assert.deepEqual(summary.legacy,['legacy','older','timestamp']);
  assert.equal(summary.first,4);
  assert.deepEqual(summary.second,['current','legacy','newer','older','timestamp']);
  assert.equal(summary.current.pid,42);
  assert.equal(summary.current.version,'1.0');
  assert.equal(summary.newer.pid,43);
  assert.equal(summary.newer.version,'v1.1');
}));

for (const scenario of ['unknown-file', 'unknown-directory', 'bad-checksum']) {
  test(`Portable preflight rejects ${scenario} before starting an executable`, {skip:process.platform !== 'win32'}, () => withTemp(temp => {
    const input = path.join(temp, 'input'), folder = path.join(input, 'SWD');
    for (const relative of JSON.parse(fs.readFileSync(path.join(scripts,'portable-files.json')))) {
      const file = path.join(folder,relative); fs.mkdirSync(path.dirname(file),{recursive:true}); fs.writeFileSync(file,'not executable: must never run');
    }
    if (scenario==='unknown-file') fs.writeFileSync(path.join(folder,'README.txt'),'obsolete delivery');
    if (scenario==='unknown-directory') fs.mkdirSync(path.join(folder,'data'));
    const archive = path.join(temp,'portable.zip');
    run('pwsh',['-NoProfile','-NonInteractive','-Command',`[IO.Compression.ZipFile]::CreateFromDirectory(${quote(input)},${quote(archive)})`]);
    fs.writeFileSync(archive+'.sha256',`${scenario==='bad-checksum'?'0'.repeat(64):sha256(archive)}  portable.zip\n`);
    const evidence = path.join(temp,'evidence');
    let failure;
    try { run('pwsh',['-NoProfile','-File',path.join(scripts,'test-portable.ps1'),'-Zip',archive,'-EvidenceDir',evidence]); }
    catch (error) { failure = String(error.stderr); }
    assert.match(failure || '', scenario==='bad-checksum' ? /checksum mismatch/ : /Unexpected Portable ZIP|Empty or unknown ZIP/);
    for (const directory of fs.readdirSync(evidence)) assert.deepEqual(fs.readdirSync(path.join(evidence,directory)),[],'untrusted layout was extracted');
  }));
}
