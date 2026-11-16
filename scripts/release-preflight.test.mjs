import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { withTemp } from './release-source.mjs';

const scripts = path.dirname(fileURLToPath(import.meta.url));
const ps = value => `'${value.replaceAll("'", "''")}'`;
function preflight({ failProduct = false, failRace = false, raceShard, nodeVersion = 'v22.23.4', goVersion = 'go version go1.27.3 windows/amd64', pnpmVersion = '11.25.0', pythonVersion = 'Python 3.12.13' } = {}) {
  return withTemp(temp => {
    const root = path.join(temp, 'repo'), directory = path.join(root, 'scripts'); fs.mkdirSync(directory, { recursive: true });
    fs.writeFileSync(path.join(root, '.node-version'), '22.23.4\n');
    fs.writeFileSync(path.join(root, '.python-version'), '3.12.13\n');
    fs.writeFileSync(path.join(root, 'go.mod'), 'module fixture\n\ngo 1.27.3\n');
    fs.mkdirSync(path.join(root, 'webui'));
    fs.writeFileSync(path.join(root, 'webui/package.json'), JSON.stringify({ packageManager: 'pnpm@11.25.0' }));
    for (const name of ['release-preflight.ps1', 'with-test-temp.ps1', 'test-race-shard.ps1']) fs.copyFileSync(path.join(scripts, name), path.join(directory, name));
    fs.writeFileSync(path.join(directory, 'test-race-all.ps1'), `param($EvidenceDirectory)
$global:calls.Add(@('race-all', $EvidenceDirectory))
${failRace ? "throw 'fixture race failure'" : 'foreach ($shard in 0..5) { & "$PSScriptRoot/test-race-shard.ps1" -Shard $shard -Shards 6 }'}
`);
    fs.writeFileSync(path.join(directory, 'package-portable.ps1'), 'param($ReleaseTag, $PreviousZip)\n$global:calls.Add(@("delivery", $ReleaseTag))\n$global:LASTEXITCODE = 0\n');
    const result = path.join(temp, 'result.json'), driver = path.join(temp, 'driver.ps1');
    fs.writeFileSync(driver, `
$ErrorActionPreference = 'Stop'
$global:calls = [Collections.Generic.List[object]]::new()
$global:contexts = [Collections.Generic.List[object]]::new()
function global:git { $global:LASTEXITCODE = 0; if ($args[0] -eq 'rev-parse') { 'a' * 40 } }
function global:node {
  $global:calls.Add(@('node') + $args); $global:LASTEXITCODE = 0
  $global:contexts.Add(@{ args = @($args); ci = $env:CI; settings = [Environment]::GetEnvironmentVariable('SWD_SETTINGS_BROWSER_EVIDENCE'); hosts = [Environment]::GetEnvironmentVariable('SWD_HOSTS_BROWSER_EVIDENCE') })
  if ($args[0] -eq '--version') { ${ps(nodeVersion)} }
  if ($args[0] -like '*go-patches.mjs') {
    if ($args[1] -eq 'list') { 'still-wanna-dance/internal/cacheproxy'; 'still-wanna-dance/internal/console'; 'still-wanna-dance/internal/example' }
    if ($args -contains '-list') { 0..11 | ForEach-Object { "TestT$_" } }
    if (${failProduct ? '$true' : '$false'} -and $args[1] -eq 'test' -and $args -contains './...') { $global:LASTEXITCODE = 17 }
  }
}
function global:pnpm { $global:calls.Add(@('pnpm') + $args); $global:LASTEXITCODE = 0; if ($args[0] -eq '--version') { ${ps(pnpmVersion)} } }
function global:uv { $global:calls.Add(@('uv') + $args); $global:LASTEXITCODE = 0; if ($args[0] -eq '--version') { 'uv 0.12.5' } elseif ($args[-1] -eq '--version') { ${ps(pythonVersion)} } }
function global:go { if ($env:GOTOOLCHAIN -cne 'local') { throw 'Go probe allows automatic toolchain downloads' }; $global:LASTEXITCODE = 0; ${ps(goVersion)} }
function global:gofmt { $global:LASTEXITCODE = 0 }
function global:fixtureGcc { $global:LASTEXITCODE = 0 }
$env:CI = 'caller-ci'; $env:CGO_ENABLED = 'before'; $env:GOTOOLCHAIN = 'before'; $env:CC = 'fixtureGcc'
$env:SWD_SETTINGS_BROWSER_EVIDENCE = 'caller-settings'; $env:SWD_HOSTS_BROWSER_EVIDENCE = 'caller-hosts'
$caught = ''
try { & ${ps(path.join(directory, 'release-preflight.ps1'))} ${raceShard === undefined ? '-ReleaseTag v1.2.3-preflight' : `-Scope race -RaceShard ${raceShard}`} } catch { $caught = $_.ToString() }
$record = Get-ChildItem -LiteralPath ${ps(path.join(root, 'artifacts/release-preflight'))} -Filter result.json -Recurse | ForEach-Object { Get-Content -Raw -LiteralPath $_.FullName | ConvertFrom-Json }
@{ calls = @($global:calls.ToArray()); contexts = @($global:contexts.ToArray()); error = $caught; record = $record; ci = $env:CI; cgo = $env:CGO_ENABLED; toolchain = $env:GOTOOLCHAIN; settings = $env:SWD_SETTINGS_BROWSER_EVIDENCE; hosts = $env:SWD_HOSTS_BROWSER_EVIDENCE } | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath ${ps(result)}
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
  const raceAll = result.calls.filter(call => call[0] === 'race-all');
  assert.equal(raceAll.length, 1); assert.match(raceAll[0][1], /release-preflight/);
  assert.equal(result.cgo, 'before'); assert.equal(result.toolchain, 'before');
  assert.equal(result.ci, 'caller-ci');
  assert.ok(result.contexts.every(call => call.ci === 'true'));
  assert.equal(result.record.environment.node, 'v22.23.4');
  assert.equal(result.record.environment.pnpm, '11.25.0');
  assert.equal(result.record.environment.python, 'Python 3.12.13');
  const pythonCheck = result.calls.find(call => call[0] === 'uv' && call.includes('unittest'));
  assert.ok(pythonCheck.includes('--managed-python') && pythonCheck.includes('3.12.13'));
  assert.ok(result.calls.some(call => call.includes('test') && call.includes('./...') && call.includes('-count=1')));
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

test('race failure retains evidence and stops remaining packages and delivery', () => {
  const result = preflight({ failRace: true });
  assert.match(result.error, /fixture race failure/);
  assert.equal(result.record.status, 'failed');
  assert.deepEqual(result.record.passed, ['tools', 'repository', 'product']);
  assert.equal(result.calls.some(call => call[0] === 'delivery' || call.includes('still-wanna-dance/internal/example')), false);
  assert.equal(result.cgo, 'before'); assert.equal(result.ci, 'caller-ci');
});

test('an explicit race shard runs only its group and shard zero checks other packages once', () => {
  for (const raceShard of [0, 3]) {
    const result = preflight({ raceShard });
    assert.equal(result.error, ''); assert.equal(result.record.status, 'passed');
    assert.deepEqual(result.record.passed, ['race']);
    assert.equal(result.calls.some(call => call[0] === 'race-all'), false);
    for (const packageName of ['./internal/cacheproxy', './internal/console']) {
      const runs = result.calls.filter(call => call.includes(packageName) && call.some(arg => arg.startsWith('-run=')));
      assert.equal(runs.length, 1);
      const pattern = new RegExp(runs[0].find(arg => arg.startsWith('-run=')).slice(5));
      for (let n = 0; n < 12; n++) assert.equal(pattern.test(`TestT${n}`), n % 6 === raceShard);
    }
    assert.equal(result.calls.filter(call => call.includes('still-wanna-dance/internal/example') && call.includes('-race')).length, raceShard === 0 ? 1 : 0);
  }
});

test('product failure stops later checks and delivery, retaining failure evidence and environment', () => {
  const result = preflight({ failProduct: true });
  assert.match(result.error, /failed \(17\)/);
  assert.equal(result.record.status, 'failed');
  assert.deepEqual(result.record.passed, ['tools', 'repository']);
  assert.equal(result.calls.some(call => call[0] === 'delivery' || call.includes('-race') || call.includes('vet')), false);
  assert.equal(result.cgo, 'before'); assert.equal(result.toolchain, 'before');
  assert.equal(result.ci, 'caller-ci');
  assert.equal(result.settings, 'caller-settings'); assert.equal(result.hosts, 'caller-hosts');
});

test('toolchain mismatches stop before affected checks and restore the caller environment', () => {
  for (const [options, expected] of [
    [{ nodeVersion: 'v24.22.1' }, /Use Node v22.23.4/],
    [{ goVersion: 'go version go1.28.0 windows/amd64' }, /Use Go 1.27.3/],
    [{ pythonVersion: 'Python 3.13.0' }, /Use Python 3.12.13/],
    [{ pnpmVersion: '11.24.0' }, /Use pnpm@11.25.0/],
  ]) {
    const result = preflight(options);
    assert.match(result.error, expected);
    assert.equal(result.record.status, 'failed');
    assert.equal(result.calls.some(call => call.includes('install') || call[0] === 'delivery'), false);
    assert.equal(result.ci, 'caller-ci');
    assert.equal(result.toolchain, 'before');
  }
});
