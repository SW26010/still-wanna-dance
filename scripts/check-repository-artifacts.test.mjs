import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { classify, check, reviewWebuiBuild } from './check-repository-artifacts.mjs';

const policy = { maxBytes: 1024, warnBytes: 512, exceptions: [] };
const webuiApp = 'internal/console/assets/webui/app.js';
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
const manifestFor = bytes => ({ schemaVersion: 1, outputs: { [webuiApp]: sha256(bytes) } });

test('large text exceptions require the exact path, hash, size, kind and a reason', () => {
  const bytes = Buffer.alloc(2048, 65);
  const exception = { path: webuiApp, kind: 'large-text', bytes: bytes.length, sha256: sha256(bytes), reason: 'Reviewed production bundle' };
  assert.deepEqual(classify(webuiApp, bytes, { ...policy, exceptions: [exception] }), {});
  for (const change of [{ path: 'other.js' }, { sha256: '0'.repeat(64) }, { bytes: 1 }, { kind: 'binary-fixture' }, { reason: '' }, { reason: '  ' }]) {
    assert.ok(classify(webuiApp, bytes, { ...policy, exceptions: [{ ...exception, ...change }] }).error);
  }
  assert.ok(classify(webuiApp, Buffer.concat([bytes, Buffer.from('x')]), { ...policy, exceptions: [exception] }).error);
});

test('build review proposes Git LF bytes without granting an exception', () => {
  const contents = Buffer.from('x'.repeat(2048) + '\r\n');
  const gitBytes = Buffer.from('x'.repeat(2048) + '\n');
  const manifest = manifestFor(gitBytes);
  // Candidate generation checks only the version and app.js hash, not the full manifest.
  assert.equal(Object.hasOwn(manifest, 'inputs'), false);
  manifest.outputs['other-output.css'] = 'invalid';
  const originalPolicy = JSON.stringify(policy);
  const review = reviewWebuiBuild(contents, policy, manifest);
  assert.equal(review.status, 'candidate');
  assert.deepEqual(review.exception, { path: webuiApp, kind: 'large-text', bytes: gitBytes.length, sha256: sha256(gitBytes), reason: '' });
  assert.deepEqual(review.manifests, [{ source: 'working tree', status: 'matches' }]);
  assert.equal(JSON.stringify(policy), originalPolicy);
  assert.ok(classify(webuiApp, gitBytes, { ...policy, exceptions: [review.exception] }).error, 'an unreviewed blank reason does not allow the bundle');
  const registered = { ...policy, exceptions: [{ ...review.exception, reason: 'Reviewed production bundle' }] };
  assert.equal(reviewWebuiBuild(contents, registered, manifest), null);
  assert.equal(reviewWebuiBuild(contents, { ...policy, exceptions: [{ ...review.exception, bytes: 1 }] }, manifest).status, 'existing-exception-needs-correction');
  const mismatch = reviewWebuiBuild(contents, policy, manifestFor(Buffer.from('another bundle')));
  assert.equal(mismatch.status, 'manifest-review-required');
  assert.equal(mismatch.manifests[0].status, 'mismatch');
  assert.equal(mismatch.exception, undefined);
  assert.equal(reviewWebuiBuild(contents, policy, null).manifests[0].status, 'missing');
  assert.equal(reviewWebuiBuild(contents, policy, {}).manifests[0].status, 'invalid');
  assert.equal(reviewWebuiBuild(contents, policy, { ...manifest, schemaVersion: 2 }).manifests[0].status, 'invalid');
  assert.equal(reviewWebuiBuild(contents, policy, { ...manifest, outputs: {} }).manifests[0].status, 'invalid');
  assert.equal(reviewWebuiBuild(contents, policy, { ...manifest, outputs: { [webuiApp]: 'invalid' } }).manifests[0].status, 'invalid');
  assert.equal(reviewWebuiBuild(Buffer.from('small'), policy, manifestFor(Buffer.from('small'))), null);
});
test('embedded dashboard exception binds the reviewed archive path, hash and size', () => {
  const file='internal/dashboard/assets/zashboard.zip',bytes=Buffer.from([80,75,0,1]);
  const exception={path:file,kind:'embedded-dashboard',bytes:bytes.length,sha256:createHash('sha256').update(bytes).digest('hex')};
  const allowed={...policy,exceptions:[exception]};
  assert.deepEqual(classify(file,bytes,allowed),{});
  assert.ok(classify(file,Buffer.concat([bytes,Buffer.from('changed')]),allowed).error);
  assert.ok(classify('other.zip',bytes,{...policy,exceptions:[{...exception,path:'other.zip'}]}).error);
});
test('content, path and hash jointly enforce fixture exceptions', () => {
  const bytes = Buffer.from('SQLite\0fixture');
  const allowed = { ...policy, exceptions: [{ path: 'fixture.sqlite', kind: 'binary-fixture', sha256: createHash('sha256').update(bytes).digest('hex') }] };
  assert.deepEqual(classify('fixture.sqlite', bytes, allowed), {});
  assert.ok(classify('innocent.txt', bytes, allowed).error);
  assert.ok(classify('fixture.sqlite', Buffer.concat([bytes, Buffer.from('changed')]), allowed).error);
  assert.ok(classify('a.svg', Buffer.from('<svg><image href="data:image/png;base64,x"/></svg>'), policy).error);
  assert.ok(classify('a.json', Buffer.alloc(2048, 65), policy).error);
});

test('product icon exceptions are limited to the exact runtime assets', () => {
  const bytes = Buffer.from([0, 0, 1, 0, 1, 0]);
  for (const iconPath of ['internal/branding/logo.ico', 'internal/branding/logo-light.ico', 'internal/branding/logo_windows_amd64.syso']) {
    const exception = { path: iconPath, kind: 'product-icon', bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex') };
    const allowed = { ...policy, exceptions: [exception] };
    assert.deepEqual(classify(iconPath, bytes, allowed), {});
    assert.ok(classify('docs/evidence/logo.ico', bytes, allowed).error);
    assert.ok(classify(iconPath, Buffer.concat([bytes, Buffer.from('changed')]), allowed).error);
    assert.ok(classify(iconPath, bytes, { ...allowed, exceptions: [{ ...exception, bytes: 999 }] }).error);
    assert.ok(classify('application.exe', bytes, { ...allowed, exceptions: [{ ...exception, path: 'application.exe' }] }).error);
    assert.ok(classify('internal/branding/other.ico', bytes, { ...allowed, exceptions: [{ ...exception, path: 'internal/branding/other.ico' }] }).error);
    assert.ok(classify('internal/branding/other.syso', bytes, { ...allowed, exceptions: [{ ...exception, path: 'internal/branding/other.syso' }] }).error);
  }
});

test('README screenshots require an exact registered image and stay below the warning budget', () => {
  const bytes = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10, 0]);
  const digest = createHash('sha256').update(bytes).digest('hex');
  for (const file of ['docs/images/webui-acceleration.png', 'docs/images/webui-library.png', 'docs/images/webui-settings-dark.png']) {
    const exception = { path: file, kind: 'readme-screenshot', bytes: bytes.length, sha256: digest };
    const allowed = { ...policy, exceptions: [exception] };
    assert.deepEqual(classify(file, bytes, allowed), {});
    assert.ok(classify(file, bytes, policy).error);
    assert.ok(classify(file, Buffer.concat([bytes, Buffer.from('changed')]), allowed).error);
    assert.ok(classify(file, bytes, { ...allowed, exceptions: [{ ...exception, bytes: 999 }] }).error);
    assert.ok(classify(file, bytes, { ...allowed, warnBytes: bytes.length - 1 }).error);
    for (const other of ['docs/evidence/screenshot.png', 'docs/images/private.png', 'docs/images/webui-library.exe']) {
      assert.ok(classify(other, bytes, { ...allowed, exceptions: [{ ...exception, path: other }] }).error);
    }
  }
});

test('category artwork is limited to exact reviewed offline WebP files', () => {
  const file='internal/console/assets/webui/category-covers/default.webp',bytes=Buffer.from([82,73,70,70,0,255]);
  const exception={path:file,kind:'category-cover',bytes:bytes.length,sha256:sha256(bytes),reason:'Reviewed offline category artwork'};
  const allowed={...policy,exceptions:[exception]};
  assert.deepEqual(classify(file,bytes,allowed),{});
  assert.ok(classify(file,Buffer.concat([bytes,Buffer.from('changed')]),allowed).error);
  for(const change of [{bytes:1},{reason:''},{path:'docs/evidence/private.webp'},{path:'internal/console/assets/webui/category-covers/private.exe'}]){
    const changed={...exception,...change};
    assert.ok(classify(changed.path,bytes,{...policy,exceptions:[changed]}).error);
  }
  assert.ok(classify(file,bytes,{...allowed,warnBytes:bytes.length-1}).error);
});

test('rejects a binary introduced then deleted before HEAD', () => {
  const cwd = mkdtempSync(path.join(os.tmpdir(), 'swd-artifact-policy-'));
  const git = (...args) => execFileSync('git', args, { cwd, windowsHide: true }).toString().trim();
  try {
    git('init', '-q'); git('config', 'user.name', 'Fixture'); git('config', 'user.email', 'fixture@example.invalid');
    writeFileSync(path.join(cwd, 'source.txt'), 'source\n'); git('add', '.'); git('commit', '-qm', 'base');
    const base = git('rev-parse', 'HEAD');
    writeFileSync(path.join(cwd, 'trace.txt'), Buffer.from([0, 1, 2])); git('add', '.'); git('commit', '-qm', 'trace');
    git('rm', '-q', 'trace.txt'); git('commit', '-qm', 'remove trace');
    const result = check({ cwd, policy, base });
    assert.equal(result.errors.length, 1); assert.equal(result.errors[0].path, 'trace.txt');
  } finally { rmSync(cwd, { recursive: true, force: true }); }
});

test('history reviews each missing WebUI blob against its own commit and retains deleted versions', () => {
  const cwd = mkdtempSync(path.join(os.tmpdir(), 'swd-artifact-review-'));
  const git = (...args) => execFileSync('git', args, { cwd, windowsHide: true }).toString().trim();
  const app = path.join(cwd, webuiApp), manifest = path.join(cwd, 'webui/build-manifest.json');
  const first = Buffer.alloc(2048, 65), missing = Buffer.alloc(2048, 66), stale = Buffer.alloc(2048, 67);
  const commit = message => { git('add', '-A'); git('commit', '-qm', message); return git('rev-parse', 'HEAD'); };
  try {
    git('init', '-q'); git('config', 'user.name', 'Fixture'); git('config', 'user.email', 'fixture@example.invalid'); git('config', 'core.autocrlf', 'false');
    writeFileSync(path.join(cwd, 'source.txt'), 'source\n'); commit('base');
    const base = git('rev-parse', 'HEAD');
    mkdirSync(path.dirname(app), { recursive: true }); mkdirSync(path.dirname(manifest), { recursive: true });
    writeFileSync(app, first); writeFileSync(manifest, JSON.stringify(manifestFor(first))); const firstCommit = commit('first');
    writeFileSync(app, missing); rmSync(manifest); const missingCommit = commit('manifest missing');
    writeFileSync(app, stale); writeFileSync(manifest, JSON.stringify(manifestFor(first))); const staleCommit = commit('manifest stale');
    writeFileSync(app, first); writeFileSync(manifest, JSON.stringify(manifestFor(first))); const repeatedCommit = commit('first again');
    rmSync(app); rmSync(manifest); commit('remove generated files');

    const result = check({ cwd, policy, base });
    assert.equal(result.errors.length, 3);
    const byHash = new Map(result.errors.map(error => [error.review.sha256, error]));
    const candidate = byHash.get(sha256(first)).review;
    assert.equal(candidate.status, 'candidate');
    assert.deepEqual(new Set(candidate.manifests.map(m => m.source)), new Set([firstCommit, repeatedCommit]));
    assert.ok(candidate.manifests.every(m => m.status === 'matches'));
    assert.equal(candidate.exception.reason, '');
    assert.deepEqual(byHash.get(sha256(missing)).review.manifests, [{ source: missingCommit, status: 'missing' }]);
    const mismatch = byHash.get(sha256(stale)).review;
    assert.deepEqual(mismatch.manifests, [{ source: staleCommit, status: 'mismatch', manifestSha256: sha256(first) }]);
    assert.equal(mismatch.status, 'manifest-review-required');
    assert.equal(mismatch.exception, undefined);
    const accepted = { ...policy, exceptions: [{ ...candidate.exception, reason: 'Reviewed first bundle' }] };
    assert.equal(check({ cwd, policy: accepted, base }).errors.length, 2);

    // The same blob appearing later with a stale manifest cannot borrow the
    // earlier matching commit's evidence to produce a clean candidate.
    writeFileSync(app, first); writeFileSync(manifest, JSON.stringify(manifestFor(stale))); const mixedCommit = commit('same blob with stale manifest');
    rmSync(app); rmSync(manifest); commit('remove again');
    const mixed = check({ cwd, policy, base }).errors.find(error => error.review.sha256 === sha256(first)).review;
    assert.equal(mixed.status, 'manifest-review-required');
    assert.equal(mixed.exception, undefined);
    assert.equal(mixed.manifests.find(m => m.source === mixedCommit).status, 'mismatch');

    mkdirSync(path.join(cwd, 'scripts'));
    const script = path.join(cwd, 'scripts/check-repository-artifacts.mjs');
    copyFileSync(new URL('./check-repository-artifacts.mjs', import.meta.url), script);
    writeFileSync(path.join(cwd, 'scripts/repository-artifacts-policy.json'), JSON.stringify({ ...policy, historyBaseline: base }));
    const cli = spawnSync(process.execPath, [script], { cwd, encoding: 'utf8', windowsHide: true });
    assert.equal(cli.status, 1);
    const fromCli = JSON.parse(cli.stdout);
    assert.equal(fromCli.errors.length, 3);
    assert.equal(fromCli.errors.find(error => error.review.sha256 === sha256(first)).review.status, 'manifest-review-required');
  } finally { rmSync(cwd, { recursive: true, force: true }); }
});

test('checks every historical blob when their combined contents exceed 256 MiB', () => {
  const cwd = mkdtempSync(path.join(os.tmpdir(), 'swd-artifact-history-'));
  const git = (...args) => execFileSync('git', args, { cwd, windowsHide: true }).toString().trim();
  try {
    git('init', '-q'); git('config', 'user.name', 'Fixture'); git('config', 'user.email', 'fixture@example.invalid');
    writeFileSync(path.join(cwd, 'source.txt'), 'source\n'); git('add', '.'); git('commit', '-qm', 'base');
    const base = git('rev-parse', 'HEAD');
    const bytes = Buffer.alloc(32 * 1024 * 1024);
    for (let i = 0; i < 8; i++) {
      bytes.fill(65 + i);
      // The rejected first version disappears from HEAD but must still be checked.
      if (i === 0) bytes[0] = 0;
      writeFileSync(path.join(cwd, 'history.txt'), bytes);
      git('add', '.'); git('commit', '-qm', `version ${i}`);
    }
    const result = check({ cwd, base, policy: { maxBytes: bytes.length, warnBytes: bytes.length, exceptions: [] } });
    assert.equal(result.blobs, 9);
    assert.deepEqual(result.errors.map(({ path, bytes, reason }) => ({ path, bytes, reason })), [{
      path: 'history.txt', bytes: bytes.length,
      reason: 'binary content requires an exact documented product or fixture exception',
    }]);
    assert.deepEqual(result.warnings, []);
  } finally { rmSync(cwd, { recursive: true, force: true }); }
});
