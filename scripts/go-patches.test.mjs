import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import os from 'node:os';
import fs from 'node:fs';
import { execFileSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';
import { fileURLToPath } from 'node:url';
import { mihomoPatch, patchManifest, readPatchedModule, runGo } from './go-patches.mjs';
import { checkExecutable, goEnv, withTemp } from './release-source.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const original = { path: mihomoPatch.module, version: mihomoPatch.version, sum: mihomoPatch.originalSum, sourceIncluded: true };

test('patch preparation needs only its pinned module in a fresh offline cache', () => withTemp(temp => {
  const module = JSON.parse(execFileSync('go', ['mod', 'download', '-json', `${mihomoPatch.module}@${mihomoPatch.version}`], { cwd: root, env: { ...process.env, ...goEnv() }, encoding: 'utf8', windowsHide: true }));
  const checkout = path.join(temp, 'checkout'), proxy = path.join(temp, 'proxy');
  fs.mkdirSync(path.join(checkout, 'patches'), { recursive: true });
  for (const file of ['go.mod', 'go.sum', mihomoPatch.replacement]) fs.copyFileSync(path.join(root, file), path.join(checkout, file));
  const target = path.join(proxy, mihomoPatch.module, '@v', mihomoPatch.version);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  for (const [suffix, file] of [['.mod', module.GoMod], ['.info', module.Info], ['.zip', module.Zip]]) fs.copyFileSync(file, target + suffix);
  const env = goEnv({ GOPROXY: pathToFileURL(proxy).href, GOSUMDB: 'off', GOPRIVATE: '', GONOPROXY: 'none', GONOSUMDB: 'none', GOMODCACHE: path.join(temp, 'modules') });
  // Listing "all" fetches unrelated graph inputs before the requested command;
  // production source archives deliberately include only selected versions.
  try {
    const selected = JSON.parse(runGo(['list', '-mod=readonly', '-m', '-json', mihomoPatch.module], checkout, env));
    assert.equal(selected.Version, mihomoPatch.version);
    assert.equal(path.posix.basename(selected.Replace.Path), patchManifest(root, [original])[0].sha256);
  } finally {
    // Go's extracted module directories are read-only on Unix. Its cleaner
    // handles those permissions; restrict it to this test's private cache.
    assert.equal(path.dirname(path.resolve(env.GOMODCACHE)), path.resolve(temp));
    execFileSync('go', ['clean', '-modcache'], { cwd: checkout, env: { ...process.env, ...env }, windowsHide: true });
  }
}));

test('patched dependency vet works from a nested Windows release checkout', { skip: process.platform !== 'win32' }, () => {
  const base = path.resolve(os.tmpdir());
  const temp = fs.mkdtempSync(path.join(base, 'swd-path-'));
  try {
    // Formal packaging and public-source verification both nest their checkout.
    // The old dependency copy made vet.exe's working directory exceed MAX_PATH.
    const checkout = path.join(temp, 'checkout'.padEnd(Math.max(8, 136 - temp.length - 1), 'x'));
    fs.mkdirSync(path.join(checkout, 'patches'), { recursive: true });
    for (const file of ['go.mod', 'go.sum', mihomoPatch.replacement]) fs.copyFileSync(path.join(root, file), path.join(checkout, file));
    assert.doesNotThrow(() => runGo(['vet', '-tags=no_easytier', mihomoPatch.module + '/transport/shadowsocks/shadowstream'], checkout));
  } finally {
    if (path.dirname(temp) !== base || !path.basename(temp).startsWith('swd-path-')) throw Error('Unsafe test cleanup path');
    fs.rmSync(temp, { recursive: true, force: true });
  }
});

test('patch source identity requires the reviewed upstream version and checksum', () => {
  const [patch] = patchManifest(root, [original]);
  assert.match(patch.sha256, /^[a-f0-9]{64}$/);
  assert.deepEqual(patchManifest(root, []), []);
  for (const change of [{ version: 'v1.19.33' }, { sum: 'h1:unreviewed' }]) assert.throws(() => patchManifest(root, [{ ...original, ...change }]), /patch base differs/);
});

test('binary provenance accepts only the reviewed replacement shape without inventing a module sum', () => {
  const [patch] = patchManifest(root, [original]);
  const fields = ['=>', `./artifacts/tmp/swd-go-123abc/${patch.sha256}`, '(devel)'];
  const module = { ...original, sum: null };
  readPatchedModule(module, fields);
  assert.equal(module.sum, null);
  assert.equal(module.patchSHA256, patch.sha256);
  for (const prefix of ['./', './../../', '../temporary directory/', '/tmp/', 'C:/Users/Build User/Temp/']) {
    const fromTemp = { ...original, sum: null };
    readPatchedModule(fromTemp, ['=>', `${prefix}swd-go-123abc/${patch.sha256}`, '(devel)']);
    assert.equal(fromTemp.patchSHA256, patch.sha256);
  }
  for (const alternate of [undefined, { ...original }, { ...module }, { ...original, path: 'example.org/other', sum: null }]) assert.throws(() => readPatchedModule(alternate, fields), /not approved/);
  for (const target of ['../arbitrary', './artifacts/tmp/swd-go-123abc/not-a-hash', 'example.org/fork']) assert.throws(() => readPatchedModule({ ...original, sum: null }, ['=>', target, '(devel)']), /not approved/);
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
