import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { withTemp } from './release-source.mjs';

const scripts = path.dirname(fileURLToPath(import.meta.url));
const ps = value => `'${value.replaceAll("'", "''")}'`;
function preflight(failProduct = false) {
  return withTemp(temp => {
    const root = path.join(temp, 'repo'), directory = path.join(root, 'scripts'); fs.mkdirSync(directory, { recursive: true });
    for (const name of ['release-preflight.ps1', 'with-test-temp.ps1', 'test-race-shard.ps1']) fs.copyFileSync(path.join(scripts, name), path.join(directory, name));
    fs.writeFileSync(path.join(directory, 'package-portable.ps1'), 'param($ReleaseTag, $PreviousZip)\n$global:calls.Add(@("delivery", $ReleaseTag))\n$global:LASTEXITCODE = 0\n');
    const result = path.join(temp, 'result.json'), driver = path.join(temp, 'driver.ps1');
    fs.writeFileSync(driver, `
$ErrorActionPreference = 'Stop'
$global:calls = [Collections.Generic.List[object]]::new()
$global:contexts = [Collections.Generic.List[object]]::new()
function global:git { $global:LASTEXITCODE = 0; if ($args[0] -eq 'rev-parse') { 'a' * 40 } }
function global:node {
  $global:calls.Add(@('node') + $args); $global:LASTEXITCODE = 0
  $global:contexts.Add(@{ args = @($args); settings = [Environment]::GetEnvironmentVariable('SWD_SETTINGS_BROWSER_EVIDENCE'); hosts = [Environment]::GetEnvironmentVariable('SWD_HOSTS_BROWSER_EVIDENCE') })
  if ($args[0] -like '*go-patches.mjs') {
    if ($args[1] -eq 'list') { 'still-wanna-dance/internal/cacheproxy'; 'still-wanna-dance/internal/console'; 'still-wanna-dance/internal/example' }
    if ($args -contains '-list') { 0..11 | ForEach-Object { "TestT$_" } }
    if (${failProduct ? '$true' : '$false'} -and $args[1] -eq 'test' -and $args -contains './...') { $global:LASTEXITCODE = 17 }
  }
}
function global:pnpm { $global:calls.Add(@('pnpm') + $args); $global:LASTEXITCODE = 0 }
function global:python { $global:calls.Add(@('python') + $args); $global:LASTEXITCODE = 0 }
function global:go { if ($env:GOTOOLCHAIN -cne 'local') { throw 'Go probe allows automatic toolchain downloads' }; $global:LASTEXITCODE = 0 }
function global:gofmt { $global:LASTEXITCODE = 0 }
function global:fixtureGcc { $global:LASTEXITCODE = 0 }
$env:CGO_ENABLED = 'before'; $env:GOTOOLCHAIN = 'before'; $env:CC = 'fixtureGcc'
$env:SWD_SETTINGS_BROWSER_EVIDENCE = 'caller-settings'; $env:SWD_HOSTS_BROWSER_EVIDENCE = 'caller-hosts'
$caught = ''
try { & ${ps(path.join(directory, 'release-preflight.ps1'))} -ReleaseTag v1.2.3-preflight } catch { $caught = $_.ToString() }
$record = Get-ChildItem -LiteralPath ${ps(path.join(root, 'artifacts/release-preflight'))} -Filter result.json -Recurse | ForEach-Object { Get-Content -Raw -LiteralPath $_.FullName | ConvertFrom-Json }
@{ calls = @($global:calls.ToArray()); contexts = @($global:contexts.ToArray()); error = $caught; record = $record; cgo = $env:CGO_ENABLED; toolchain = $env:GOTOOLCHAIN; settings = $env:SWD_SETTINGS_BROWSER_EVIDENCE; hosts = $env:SWD_HOSTS_BROWSER_EVIDENCE } | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath ${ps(result)}
`);
    const child = spawnSync('pwsh', ['-NoProfile', '-File', driver], { encoding: 'utf8', windowsHide: true });
    assert.equal(child.status, 0, child.stdout + child.stderr);
    return JSON.parse(fs.readFileSync(result, 'utf8').replace(/^\uFEFF/, ''));
  });
}

test('full preflight covers every race test once and builds delivery after all checks', () => {
  const result = preflight();
  assert.equal(result.error, '');
  assert.equal(result.record.status, 'passed');
  assert.deepEqual(result.record.passed, ['tools', 'repository', 'product', 'race', 'delivery']);
  assert.equal(result.calls.at(-1)[0], 'delivery');
  assert.equal(result.cgo, 'before'); assert.equal(result.toolchain, 'before');
  assert.equal(result.settings, 'caller-settings'); assert.equal(result.hosts, 'caller-hosts');
  const integrationFlag = '-run=^(TestSettingsSeparateBrowsers|TestHostsPolicyBrowser)$';
  const integration = result.contexts.filter(call => call.args.includes(integrationFlag));
  assert.equal(integration.length, 1);
  assert.match(integration[0].settings, /settings-browser$/); assert.match(integration[0].hosts, /hosts-browser$/);
  for (const call of result.contexts.filter(call => !call.args.includes(integrationFlag))) {
    assert.equal(call.settings ?? '', ''); assert.equal(call.hosts ?? '', '');
  }
  for (const packageName of ['./internal/cacheproxy', './internal/console']) {
    const runs = result.calls.filter(call => call.includes('-race') && call.includes(packageName) && call.some(arg => arg.startsWith('-run=')));
    assert.equal(runs.length, 6);
    for (let n = 0; n < 12; n++) assert.equal(runs.filter(call => new RegExp(call.find(arg => arg.startsWith('-run=')).slice(5)).test(`TestT${n}`)).length, 1);
  }
  assert.equal(result.calls.filter(call => call.includes('still-wanna-dance/internal/example') && call.includes('-race')).length, 1);
});

test('product failure stops later checks and delivery, retaining failure evidence and environment', () => {
  const result = preflight(true);
  assert.match(result.error, /failed \(17\)/);
  assert.equal(result.record.status, 'failed');
  assert.deepEqual(result.record.passed, ['tools', 'repository']);
  assert.equal(result.calls.some(call => call[0] === 'delivery' || call.includes('-race') || call.includes('vet')), false);
  assert.equal(result.cgo, 'before'); assert.equal(result.toolchain, 'before');
  assert.equal(result.settings, 'caller-settings'); assert.equal(result.hosts, 'caller-hosts');
});
