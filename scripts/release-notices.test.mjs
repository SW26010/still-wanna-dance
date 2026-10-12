import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { checkNotices, validateNotices } from './release-notices.mjs';
import { readJSON } from './release-source.mjs';

test('notice gates reject removed text, unreviewed modules/checksums, and allow accepted go-lzo', () => {
  const mod = { path: 'github.com/rasky/go-lzo', version: 'v0.0.0-20200203143853-96a758eda86e', sum: 'h1:fixture', licenseFiles: [{ path: 'LICENSE.gpl' }] };
  const text = `Module: ${mod.path} ${mod.version}\nGPL text\n`;
  const review = { schemaVersion: 1, noticesSHA256: crypto.createHash('sha256').update(text).digest('hex'), modules: [mod] };
  assert.doesNotThrow(() => checkNotices(text, review, [mod]));
  assert.doesNotThrow(() => checkNotices(text.replaceAll('\n', '\r\n'), review, [mod]));
  assert.throws(() => checkNotices(text.replace('GPL text', ''), review, [mod]), /contents differ/);
  for (const change of [{ version: 'v9.9.9' }, { sum: 'h1:changed' }, { path: 'unreviewed/module' }]) assert.throws(() => checkNotices(text, review, [{ ...mod, ...change }]), /Missing reviewed/);
});

test('committed notices cover every reviewed pinned desktop/adapter module', () => {
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
  validateNotices(root, readJSON(path.join(root, 'docs/licenses/reviewed-modules.json')).modules);
});
