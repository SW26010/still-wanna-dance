import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
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
test('public source must be independently accessible and match the bound bytes', async () => {
  await withTemp(async temp => {
    const requested = [];
    await downloadAssets(fixture(), temp, async url => { requested.push(url); return new Response(content); });
    assert.equal(requested.length, 2); assert.ok(requested[1].endsWith('-corresponding-source.tar.gz'));
    assert.deepEqual(fs.readFileSync(path.join(temp, fixture().assets[1].name)), content);
  });
  await withTemp(async temp => {
    await assert.rejects(downloadAssets(fixture(), temp, async url => url.endsWith('.zip') ? new Response(content) : new Response('missing', { status: 404 })), /source asset is unavailable/);
  });
  await withTemp(async temp => {
    await assert.rejects(downloadAssets(fixture(), temp, async () => new Response('tampered')), /differs/);
  });
});
test('local verification rejects missing or corrupt source before extraction', () => withTemp(temp => {
  const m = fixture(); fs.writeFileSync(path.join(temp, m.assets[0].name), content);
  assert.throws(() => verifyRelease({ manifest: m, directory: temp }), /ENOENT/);
  fs.writeFileSync(path.join(temp, m.assets[1].name), 'corrupt');
  assert.throws(() => verifyRelease({ manifest: m, directory: temp }), /checksum mismatch/);
}));
