import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { withTemp, repository } from './release-source.mjs';
import { assetURL, releaseNames, checkReleaseManifest, downloadAssets, verifyRelease } from './release.mjs';

const content = Buffer.from('test asset');
const hash = crypto.createHash('sha256').update(content).digest('hex');
function fixture() {
  const tag = 'v1.2.3', names = releaseNames(tag);
  return { schemaVersion: 1, kind: 'formal', repository, tag, revision: 'a'.repeat(40), target: 'windows/amd64', toolchain: 'go1.25.0', executableSHA256: hash, modules: [], acceptedRisks: [], independentRebuild: { verified: true, executableSHA256: hash, bitReproducible: false }, assets: ['portable', 'source'].map(kind => ({ kind, name: names[kind], sha256: hash, bytes: content.length, url: assetURL(tag, names[kind]) })) };
}
test('formal manifest binds both exact release assets and rebuild evidence', () => {
  const m = fixture(); assert.doesNotThrow(() => checkReleaseManifest(m));
  for (const patch of [{ kind: 'development' }, { tag: 'dirty' }, { revision: '' }, { assets: [m.assets[0]] }, { assets: [m.assets[0], m.assets[0]] }, { independentRebuild: { verified: false } }, { acceptedRisks: undefined }, { acceptedRisks: [{ status: 'unreviewed' }] }]) assert.throws(() => checkReleaseManifest({ ...m, ...patch }));
  for (const patch of [{ name: '../source.tar.gz' }, { sha256: 'bad' }, { url: 'https://example.invalid/source' }, { bytes: 0 }]) assert.throws(() => checkReleaseManifest({ ...m, assets: [m.assets[0], { ...m.assets[1], ...patch }] }));
});
test('published legacy risk records remain verifiable without allowing other exceptions', () => {
  const legacy = { module: 'github.com/rasky/go-lzo', version: 'v0.0.0-20200203143853-96a758eda86e', status: 'accepted-nonblocking', acceptedOn: '2026-10-12', evidence: 'source/docs/issue-30-license-audit.md' };
  assert.doesNotThrow(() => checkReleaseManifest({ ...fixture(), acceptedRisks: [legacy] }));
  for (const patch of [{ module: 'unreviewed/module' }, { version: 'v9.0.0' }, { status: 'unreviewed' }]) assert.throws(() => checkReleaseManifest({ ...fixture(), acceptedRisks: [{ ...legacy, ...patch }] }));
});
test('all public release assets are accessible and match the verified delivery', async () => {
  const m = fixture();
  const published = () => new Map([
    ...m.assets.flatMap(a => [[a.url, content], [a.url + '.sha256', `${a.sha256}  ${a.name}\n`]]),
    [assetURL(m.tag, releaseNames(m.tag).manifest), JSON.stringify(m)],
  ]);
  const fetchFrom = (files, requested = []) => async url => {
    requested.push(url);
    return files.has(url) ? new Response(files.get(url)) : new Response('missing', { status: 404 });
  };
  await withTemp(async temp => {
    const requested = [];
    await downloadAssets(m, temp, fetchFrom(published(), requested));
    assert.equal(requested.length, 5);
    assert.deepEqual(fs.readFileSync(path.join(temp, m.assets[1].name)), content);
  });
  for (const [url, value, error] of [
    [m.assets[1].url, null, /source asset is unavailable/],
    [m.assets[0].url, 'tampered', /differs/],
    [m.assets[1].url + '.sha256', 'wrong checksum', /source checksum/],
    [assetURL(m.tag, releaseNames(m.tag).manifest), JSON.stringify({ ...m, revision: 'b'.repeat(40) }), /manifest.*differs/],
  ]) {
    await withTemp(async temp => {
      const files = published();
      if (value === null) files.delete(url); else files.set(url, value);
      await assert.rejects(downloadAssets(m, temp, fetchFrom(files)), error);
    });
  }
});
test('public asset-only CLI needs neither a toolchain nor another rebuild', () => withTemp(temp => {
  const m = fixture();
  const manifest = path.join(temp, 'manifest.json'), preload = path.join(temp, 'fetch.mjs');
  fs.writeFileSync(manifest, JSON.stringify(m));
  const files = [
    ...m.assets.flatMap(a => [[a.url, content.toString()], [a.url + '.sha256', `${a.sha256}  ${a.name}\n`]]),
    [assetURL(m.tag, releaseNames(m.tag).manifest), JSON.stringify(m)],
  ];
  fs.writeFileSync(preload, `const files = new Map(${JSON.stringify(files)}); globalThis.fetch = async url => files.has(url) ? new Response(files.get(url)) : new Response('missing', {status: 404});`);
  const script = fileURLToPath(new URL('./release.mjs', import.meta.url));
  const child = spawnSync(process.execPath, ['--import', pathToFileURL(preload).href, script, 'verify-published-assets', '--manifest', manifest], { encoding: 'utf8', env: { ...process.env, PATH: '' }, windowsHide: true });
  assert.equal(child.status, 0, child.stderr);
  const result = JSON.parse(child.stdout.trim().split(/\r?\n/).at(-1));
  assert.deepEqual(result, { tag: m.tag, revision: m.revision, verifiedAssets: true });
}));
test('local verification rejects missing or corrupt source before extraction', () => withTemp(temp => {
  const m = fixture(); fs.writeFileSync(path.join(temp, m.assets[0].name), content);
  assert.throws(() => verifyRelease({ manifest: m, directory: temp }), /ENOENT/);
  fs.writeFileSync(path.join(temp, m.assets[1].name), 'corrupt');
  assert.throws(() => verifyRelease({ manifest: m, directory: temp }), /checksum mismatch/);
}));
