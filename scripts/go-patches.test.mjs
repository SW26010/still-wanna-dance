import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { mihomoPatch, patchManifest, readPatchedModule } from './go-patches.mjs';
import { checkExecutable } from './release-source.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const original = { path: mihomoPatch.module, version: mihomoPatch.version, sum: mihomoPatch.originalSum, sourceIncluded: true };

test('patch source identity requires the reviewed upstream version and checksum', () => {
  const [patch] = patchManifest(root, [original]);
  assert.match(patch.sha256, /^[a-f0-9]{64}$/);
  assert.deepEqual(patchManifest(root, []), []);
  for (const change of [{ version: 'v1.19.33' }, { sum: 'h1:unreviewed' }]) assert.throws(() => patchManifest(root, [{ ...original, ...change }]), /patch base differs/);
});

test('binary provenance accepts only the reviewed replacement shape without inventing a module sum', () => {
  const [patch] = patchManifest(root, [original]);
  const fields = ['=>', `./artifacts/go-patches/build-123abc/mihomo-anytls-${patch.sha256}`, '(devel)'];
  const module = { ...original, sum: null };
  readPatchedModule(module, fields);
  assert.equal(module.sum, null);
  assert.equal(module.patchSHA256, patch.sha256);
  for (const alternate of [undefined, { ...original }, { ...module }, { ...original, path: 'example.org/other', sum: null }]) assert.throws(() => readPatchedModule(alternate, fields), /not approved/);
  for (const target of ['../arbitrary', './artifacts/go-patches/build-123abc/mihomo-anytls-not-a-hash', 'example.org/fork']) assert.throws(() => readPatchedModule({ ...original, sum: null }, ['=>', target, '(devel)']), /not approved/);
});

test('delivery rejects a missing or different binary patch and an unpatched binary', () => {
  const [patch] = patchManifest(root, [original]);
  const manifest = { toolchain: 'go1.27.3', revision: 'a'.repeat(40), modules: [original], goPatches: [patch] };
  const info = { entry: 'still-wanna-dance/cmd/still-wanna-dance-console', toolchain: manifest.toolchain,
    settings: { GOOS: 'windows', GOARCH: 'amd64', GOAMD64: 'v1', CGO_ENABLED: '0', '-trimpath': 'true', '-tags': 'no_easytier', 'vcs.revision': manifest.revision, 'vcs.modified': 'false' },
    modules: [{ ...original, sum: null, patchSHA256: patch.sha256 }] };
  assert.doesNotThrow(() => checkExecutable(info, manifest));
  for (const module of [original, { ...info.modules[0], patchSHA256: '0'.repeat(64) }]) assert.throws(() => checkExecutable({ ...info, modules: [module] }, manifest), /dependency differs/);
  assert.throws(() => checkExecutable(info, { ...manifest, goPatches: [] }), /dependency differs/);
});
