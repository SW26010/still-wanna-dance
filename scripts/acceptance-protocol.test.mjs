import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { AcceptanceBlocked, requireAcceptedTerms } from './acceptance-protocol.mjs';
import { withTemp } from './release-source.mjs';

test('consent is verified by the application, with missing consent classified as blocked', () => {
  assert.doesNotThrow(() => requireAcceptedTerms(200));
  assert.throws(() => requireAcceptedTerms(428), e => e instanceof AcceptanceBlocked && e.category === 'environment');
  for (const status of [302, 403, 500]) {
    assert.throws(() => requireAcceptedTerms(status), e => !(e instanceof AcceptanceBlocked));
  }
});

test('acceptance accepts a receipt alone or after an explicit executable', () => withTemp(cwd => {
  fs.writeFileSync(path.join(cwd, 'terms.json'), 'synthetic fixture');
  const script = fileURLToPath(new URL('./acceptance.mjs', import.meta.url));
  for (const [args, message] of [
    [['terms.json'], 'Missing console binary: bin/SWD.exe'],
    [['custom/SWD.exe', 'terms.json'], 'Missing console binary: custom/SWD.exe'],
    [[], 'Provide an existing console terms.json receipt'],
  ]) {
    const result = spawnSync(process.execPath, [script, ...args], { cwd, encoding: 'utf8', windowsHide: true });
    assert.equal(result.status, 2, result.stderr || result.stdout);
    assert.ok(result.stdout.includes(message), result.stdout);
  }
}));
