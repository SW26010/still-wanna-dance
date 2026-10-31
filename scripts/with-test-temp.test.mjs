import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scripts = path.dirname(fileURLToPath(import.meta.url));
const tempRoot = path.resolve(process.env.SWD_TEST_TEMP_ROOT || path.join(scripts, '../artifacts/tmp'));
const windows = { skip: process.platform !== 'win32' };
const quote = value => "'" + value.replaceAll("'", "''") + "'";
function powershell(source) {
  const result = spawnSync('pwsh', ['-NoProfile', '-NonInteractive', '-Command', source], {
    cwd: scripts,
    env: { ...process.env, SWD_TEST_NODE: process.execPath },
    encoding: 'utf8', windowsHide: true, timeout: 30_000,
  });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  return JSON.parse(result.stdout);
}
const snapshot = "@($env:TEMP, $env:TMP, $env:GOTMPDIR, $env:SWD_TEST_TEMP_ROOT)";
const probe = `
  const fs = require('node:fs'), path = require('node:path');
  const temp = process.env.TEMP;
  fs.writeFileSync(path.join(temp, 'old.txt'), 'old');
  fs.writeFileSync(path.join(temp, 'new.txt'), 'new');
  fs.renameSync(path.join(temp, 'new.txt'), path.join(temp, 'old.txt'));
  console.log(JSON.stringify({ temp, tmp: process.env.TMP, go: process.env.GOTMPDIR,
    cwd: process.cwd(), args: process.argv.slice(1), text: fs.readFileSync(path.join(temp, 'old.txt'), 'utf8') }));
`;

test('temporary environment supports atomic replacement and preserves arguments, cwd and exit status', windows, () => {
  const paths = [];
  for (const code of [0, 23]) {
    const result = powershell(`
      $env:GOTMPDIR = $null
      $before = ${snapshot}
      $PSNativeCommandUseErrorActionPreference = $true
      $child = & ./with-test-temp.ps1 $env:SWD_TEST_NODE -e ${quote(probe + `process.exit(${code});`)} -- 'two words' '-count=1' '中文' ''
      $code = $LASTEXITCODE
      @{ child = ($child | ConvertFrom-Json); code = $code; before = $before; after = ${snapshot} } | ConvertTo-Json -Compress -Depth 4
    `);
    assert.equal(result.code, code);
    assert.deepEqual(result.after, result.before);
    assert.equal(path.dirname(result.child.temp), tempRoot);
    assert.equal(result.child.tmp, result.child.temp);
    assert.equal(result.child.go, result.child.temp);
    assert.equal(result.child.cwd, scripts);
    assert.deepEqual(result.child.args, ['two words', '-count=1', '中文', '']);
    assert.equal(result.child.text, 'new');
    assert.equal(existsSync(result.child.temp), false);
    paths.push(result.child.temp);
  }
  assert.notEqual(paths[0], paths[1]);
  const shell = powershell(`
    & ./with-test-temp.ps1 pwsh -NoProfile -NonInteractive -Command 'exit 19'
    @{ code = $LASTEXITCODE } | ConvertTo-Json -Compress
  `);
  assert.equal(shell.code, 19);
});

test('terminating command errors restore the environment and remove only their temporary directory', windows, () => {
  const result = powershell(`
    $before = ${snapshot}
    function Invoke-FailingFixture {
      $global:observedTemp = $env:TEMP
      Set-Content -LiteralPath (Join-Path $env:TEMP 'leftover.txt') -Value 'fixture'
      throw 'fixture failure'
    }
    try { & ./with-test-temp.ps1 Invoke-FailingFixture; throw 'expected failure' }
    catch { $failure = $_.Exception.Message }
    @{ before = $before; after = ${snapshot}; temp = $global:observedTemp; failure = $failure } | ConvertTo-Json -Compress
  `);
  assert.equal(result.failure, 'fixture failure');
  assert.deepEqual(result.after, result.before);
  assert.equal(path.dirname(result.temp), tempRoot);
  assert.equal(existsSync(result.temp), false);
});

test('a single script argument is forwarded intact', windows, () => {
  const result = powershell(`
    $script = Join-Path $env:TEMP ('single argument 中文 ' + [Guid]::NewGuid().ToString('N') + '.cjs')
    try {
      [IO.File]::WriteAllText($script, ${quote(probe)})
      $child = & ./with-test-temp.ps1 $env:SWD_TEST_NODE $script
      @{ code = $LASTEXITCODE; child = ($child | ConvertFrom-Json) } | ConvertTo-Json -Compress -Depth 4
    } finally { Remove-Item -LiteralPath $script -Force }
  `);
  assert.equal(result.code, 0);
  assert.equal(result.child.text, 'new');
  assert.equal(path.dirname(result.child.temp), tempRoot);
  assert.equal(existsSync(result.child.temp), false);
});

test('nested invocations use separate directories without deleting the caller temporary files', windows, () => {
  const result = powershell(`
    function Invoke-OuterFixture {
      $outer = $env:TEMP
      $sentinel = Join-Path $outer 'keep.txt'
      Set-Content -LiteralPath $sentinel -Value 'outer'
      $child = & ./with-test-temp.ps1 $env:SWD_TEST_NODE -e ${quote(probe)}
      @{ outer = $outer; restored = $env:TEMP; sentinel = (Test-Path -LiteralPath $sentinel); child = ($child | ConvertFrom-Json) } | ConvertTo-Json -Compress -Depth 4
    }
    & ./with-test-temp.ps1 Invoke-OuterFixture
  `);
  assert.equal(result.restored, result.outer);
  assert.equal(result.sentinel, true);
  assert.notEqual(result.outer, result.child.temp);
  assert.equal(existsSync(result.outer), false);
  assert.equal(existsSync(result.child.temp), false);
});

test('a wrapper copied into a release checkout keeps nested temporary directories beside its caller', windows, () => {
  const result = powershell(`
    function Invoke-OuterFixture {
      $outer = $env:TEMP
      $checkout = Join-Path $outer 'source-checkout/scripts'
      New-Item -ItemType Directory -Path $checkout -Force | Out-Null
      $copy = Join-Path $checkout 'with-test-temp.ps1'
      Copy-Item -LiteralPath ./with-test-temp.ps1 -Destination $copy
      $child = & $copy $env:SWD_TEST_NODE -e ${quote(probe)}
      @{ outer = $outer; restored = $env:TEMP; child = ($child | ConvertFrom-Json) } | ConvertTo-Json -Compress -Depth 4
    }
    & ./with-test-temp.ps1 Invoke-OuterFixture
  `);
  assert.equal(result.restored, result.outer);
  assert.equal(path.dirname(result.child.temp), path.dirname(result.outer));
  assert.notEqual(result.outer, result.child.temp);
  assert.equal(existsSync(result.outer), false);
  assert.equal(existsSync(result.child.temp), false);
});

test('failed portable smoke retains extracted evidence after wrapper and checkout cleanup', windows, () => {
  const evidenceRoot = path.resolve(scripts, '../artifacts/portable-smoke');
  for (const checkout of [false, true]) {
    const child = `
      $ErrorActionPreference = 'Stop'
      Write-Output "Command temporary: $env:TEMP"
      $source = ${quote(path.join(scripts, 'test-portable.ps1'))}
      $smokeInput = Join-Path $env:TEMP 'input'
      New-Item -ItemType Directory -Path (Join-Path $smokeInput 'SWD/licenses') -Force | Out-Null
      $inventory = ${quote(path.join(scripts, 'portable-files.json'))}
      foreach ($name in (Get-Content -LiteralPath $inventory -Raw | ConvertFrom-Json)) {
        [IO.File]::WriteAllText((Join-Path $smokeInput "SWD/$name"), 'smoke fixture')
      }
      $zip = Join-Path $env:TEMP 'invalid-portable.zip'
      [IO.Compression.ZipFile]::CreateFromDirectory($smokeInput, $zip)
      $hash = (Get-FileHash -LiteralPath $zip).Hash.ToLowerInvariant()
      [IO.File]::WriteAllText("$zip.sha256", "$hash  invalid-portable.zip")
      ${checkout ? `
        $checkout = Join-Path $env:TEMP 'checkout/scripts'
        New-Item -ItemType Directory -Path $checkout -Force | Out-Null
        $copy = Join-Path $checkout 'test-portable.ps1'
        Copy-Item -LiteralPath $source -Destination $copy
        Copy-Item -LiteralPath $inventory -Destination (Join-Path $checkout 'portable-files.json')
        & $copy -Zip $zip -EvidenceDir ${quote(evidenceRoot)}
      ` : '& $source -Zip $zip'}
    `;
    const result = powershell(`
      $output = & ./with-test-temp.ps1 pwsh -NoProfile -NonInteractive -Command ${quote(child)} 2>&1
      $code = $LASTEXITCODE
      @{ code = $code; output = ($output | Out-String) } | ConvertTo-Json -Compress
    `);
    assert.notEqual(result.code, 0);
    assert.match(result.output, /Cannot read executable build identity/);
    const temporary = result.output.match(/Command temporary: ([^\r\n]+)/)?.[1].trim();
    const evidence = result.output.match(/Smoke test files: ([^\r\n]+)/)?.[1].trim();
    assert.ok(temporary, result.output);
    assert.ok(evidence, result.output);
    assert.equal(path.dirname(evidence), evidenceRoot);
    assert.match(path.basename(evidence), /^Still Wanna Dance smoke [a-f0-9]{32}$/);
    try {
      assert.equal(existsSync(temporary), false);
      assert.equal(readFileSync(path.join(evidence, 'SWD/licenses/SOURCE.txt'), 'utf8'), 'smoke fixture');
    } finally {
      // The checks above bound cleanup to this fixture's unique evidence directory.
      rmSync(evidence, { recursive: true, force: true });
    }
  }
});
