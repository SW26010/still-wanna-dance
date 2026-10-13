import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { pathToFileURL } from 'node:url';
import { withTemp, run, jsonStream, safePath, cleanRevision, createSource, extractSource, verifyBundle, readJSON, writeJSON, rebuild, checkExecutable, supplementalInputs } from './release-source.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
function fixture(temp) {
  const root = path.join(temp, 'repo'); fs.mkdirSync(root);
  for (const [name, text] of Object.entries({ 'go.mod': 'module still-wanna-dance\n\ngo 1.25.0\n', 'go.sum': '', LICENSE: 'fixture GPL', 'LICENSE-MIT': 'fixture MIT', 'LICENSING.md': 'fixture policy', 'THIRD-PARTY-NOTICES.txt': 'fixture notices', 'scripts/build-desktop.ps1': '# fixture', 'cmd/still-wanna-dance-console/main.go': 'package main\nfunc main() {}\n', 'docs/evidence/old.json': '{}', 'experiments/probe.go': 'historical probe', 'docs/research/chart.json': '[1,2,3]', 'scripts/render-chart.py': '# source for an embedded chart' })) {
    const dest = path.join(root, name); fs.mkdirSync(path.dirname(dest), { recursive: true }); fs.writeFileSync(dest, text);
  }
  fs.copyFileSync(path.join(here, 'release-source.mjs'), path.join(root, 'scripts/release-source.mjs'));
  fs.copyFileSync(path.join(here, '../.gitattributes'), path.join(root, '.gitattributes'));
  run('git', ['init', '--quiet'], root);
  run('git', ['config', 'core.autocrlf', 'false'], root);
  run('git', ['add', '.'], root);
  run('git', ['-c', 'user.name=SWD test', '-c', 'user.email=test@example.invalid', 'commit', '--quiet', '-m', 'fixture'], root);
  run('git', ['tag', 'v0.0.0-test'], root);
  return root;
}

test('Go JSON stream handles nested and escaped strings and rejects incomplete input', () => {
  assert.deepEqual(jsonStream('{"a":{"b":"}\\\""}}\n{"c":1}'), [{ a: { b: '}"' } }, { c: 1 }]);
  for (const value of ['x{}', '{', '{"a":"', '}']) assert.throws(() => jsonStream(value));
});

test('source paths cannot escape or target Windows alternate streams', () => {
  for (const name of ['../x', '/x', 'a/../../b', 'C:/x', 'a:b', 'a\\b', './a', 'a//b', 'a\nx']) assert.throws(() => safePath(here, name));
  assert.equal(safePath(here, 'a/b'), path.join(here, 'a/b'));
});

test('formal source requires clean worktree and tag at HEAD', () => withTemp(temp => {
  const root = fixture(temp);
  assert.match(cleanRevision(root, 'v0.0.0-test'), /^[a-f0-9]{40}$/);
  assert.throws(() => cleanRevision(root, 'dev'), /version tag/);
  fs.writeFileSync(path.join(root, 'untracked'), 'dirty');
  assert.throws(() => cleanRevision(root, 'v0.0.0-test'), /clean/);
  run('git', ['add', '.'], root);
  run('git', ['-c', 'user.name=SWD test', '-c', 'user.email=test@example.invalid', 'commit', '--quiet', '-m', 'new head'], root);
  assert.throws(() => cleanRevision(root, 'v0.0.0-test'), /HEAD/);
}));

test('complete source archive rebuilds without Git metadata or populated Go caches', { timeout: 180000 }, () => withTemp(temp => {
  const root = fixture(temp);
  const result = createSource({ root, tag: 'v0.0.0-test', outputDir: path.join(temp, 'out') });
  const bundle = extractSource(result.archive, path.join(temp, 'extracted'));
  assert.equal(fs.existsSync(path.join(bundle, 'source/.git')), false);
  for (const omitted of ['docs/evidence/old.json', 'experiments/probe.go']) {
    assert.ok(fs.existsSync(path.join(root, omitted)), 'history remains in the repository');
    assert.equal(fs.existsSync(path.join(bundle, 'source', omitted)), false);
  }
  for (const kept of ['docs/research/chart.json', 'scripts/render-chart.py']) assert.ok(fs.existsSync(path.join(bundle, 'source', kept)));
  const output = path.join(temp, 'rebuilt.exe');
  assert.equal(rebuild({ bundle, output }).manifest.revision, result.manifest.revision);
  assert.ok(fs.statSync(output).size > 0);
  assert.throws(() => createSource({ root, tag: 'v0.0.0-test', outputDir: path.join(temp, 'out') }), /already exists/);
  const manifestFile = path.join(bundle, 'source-manifest.json');
  const original = readJSON(manifestFile);
  const license = path.join(bundle, 'source/LICENSE'); const originalLicense = fs.readFileSync(license);
  fs.appendFileSync(license, 'tampered');
  assert.throws(() => verifyBundle(bundle), /mismatch/);
  fs.writeFileSync(license, originalLicense);
  writeJSON(manifestFile, { ...original, files: original.files.filter(f => f.path !== 'source/LICENSE') });
  assert.throws(() => verifyBundle(bundle), /Missing source input/);
  writeJSON(manifestFile, { ...original, files: [...original.files, { path: '../outside', bytes: 0, sha256: '' }] });
  assert.throws(() => verifyBundle(bundle), /Unsafe path/);
  writeJSON(manifestFile, original);
  fs.writeFileSync(path.join(bundle, 'source/unlisted.go'), 'package main');
  assert.throws(() => verifyBundle(bundle), /unmanifested/);
}));

test('unused graph modules need metadata only, even when their ZIP is unavailable', { timeout: 180000 }, () => withTemp(temp => {
  const previous = Object.fromEntries(['GOPROXY', 'GOSUMDB', 'GOMODCACHE'].map(k => [k, process.env[k]]));
  try {
    const proxy = path.join(temp, 'metadata-proxy'), base = path.join(proxy, 'example.org/unused/@v');
    fs.mkdirSync(base, { recursive: true });
    fs.writeFileSync(path.join(base, 'v1.0.0.mod'), 'module example.org/unused\n\ngo 1.25.0\n');
    writeJSON(path.join(base, 'v1.0.0.info'), { Version: 'v1.0.0', Time: '2026-01-01T00:00:00Z' });
    process.env.GOPROXY = pathToFileURL(proxy).href;
    process.env.GOSUMDB = 'off';
    process.env.GOMODCACHE = path.join(temp, 'empty-modules');
    const root = fixture(temp);
    fs.appendFileSync(path.join(root, 'go.mod'), '\nrequire example.org/unused v1.0.0\n');
    run('go', ['list', '-mod=mod', '-m', 'all'], root, { GOWORK: 'off', GOTOOLCHAIN: 'local' });
    run('git', ['add', '.'], root);
    run('git', ['-c', 'user.name=SWD test', '-c', 'user.email=test@example.invalid', 'commit', '--quiet', '-m', 'unused dependency'], root);
    run('git', ['tag', 'v0.0.1-test'], root);
    const result = createSource({ root, tag: 'v0.0.1-test', outputDir: path.join(temp, 'out') });
    const bundle = extractSource(result.archive, path.join(temp, 'extracted'));
    assert.equal(result.manifest.modules.length, 1);
    const mod = result.manifest.modules[0];
    assert.equal(mod.sourceIncluded, false);
    assert.equal(mod.sum, null);
    assert.equal(fs.existsSync(path.join(bundle, mod.proxyBase + '.zip')), false);
    assert.ok(fs.existsSync(path.join(bundle, mod.proxyBase + '.mod')));
    rebuild({ bundle, output: path.join(temp, 'rebuilt.exe') });
  } finally {
    for (const [key, value] of Object.entries(previous)) if (value === undefined) delete process.env[key]; else process.env[key] = value;
  }
}));

test('binary identity rejects wrong revision, dirty state and unrecorded module versions; accepted go-lzo is allowed', () => {
  const mod = { path: 'github.com/rasky/go-lzo', version: 'v0.0.0-20200203143853-96a758eda86e', sum: 'h1:fixture', sourceIncluded: true };
  const manifest = { revision: 'a'.repeat(40), toolchain: 'go1.25.0', modules: [mod] };
  const info = { entry: 'still-wanna-dance/cmd/still-wanna-dance-console', toolchain: 'go1.25.0', modules: [mod], settings: { GOOS: 'windows', GOARCH: 'amd64', GOAMD64: 'v1', CGO_ENABLED: '0', '-trimpath': 'true', '-tags': 'no_easytier', 'vcs.revision': manifest.revision, 'vcs.modified': 'false' } };
  assert.doesNotThrow(() => checkExecutable(info, manifest));
  for (const change of [{ 'vcs.modified': 'true' }, { 'vcs.revision': 'b'.repeat(40) }, { GOOS: 'linux' }, { GOAMD64: 'v3' }, { '-tags': '' }]) assert.throws(() => checkExecutable({ ...info, settings: { ...info.settings, ...change } }, manifest));
  const easytier = { ...mod, path: 'github.com/easytier/easytier/easytier-go' };
  assert.throws(() => checkExecutable({ ...info, modules: [easytier] }, { ...manifest, modules: [easytier] }), /EasyTier must be excluded/);
  assert.throws(() => checkExecutable({ ...info, modules: [{ ...mod, version: 'v1.0.0' }] }, manifest), /dependency differs/);
  assert.throws(() => checkExecutable(info, { ...manifest, modules: [{ ...mod, sourceIncluded: false }] }), /dependency differs/);
});

test('embedded artifacts need their original source; accepted go-lzo uncertainty is independent', () => {
  const policy = readJSON(path.join(here, '../docs/licenses/source-inputs.json'));
  assert.deepEqual(supplementalInputs(policy, [{ path: 'github.com/rasky/go-lzo', version: 'v0.0.0-20200203143853-96a758eda86e' }]), []);
  assert.throws(() => supplementalInputs(policy, [{ path: 'github.com/easytier/easytier/easytier-go', version: 'v0.0.0-20260910071355-3d0c9c3ca5e2' }]), /Incomplete corresponding source/);
  assert.equal(supplementalInputs(policy, [{ path: 'modernc.org/sqlite', version: 'v1.40.1' }]).length, 2);
  assert.throws(() => supplementalInputs(policy, [{ path: 'modernc.org/sqlite', version: 'v9.0.0' }]), /review required/);
});
