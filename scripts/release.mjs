// SPDX-License-Identifier: GPL-3.0-only
// Local preparation/verification only. Publication is a separate operator step.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { run, goEnv, sha256, readJSON, writeJSON, filesUnder, withTemp, cleanRevision, createSource, extractSource, extractArchive, executableInfo, checkExecutable, rebuild, repository, desktopEntry, desktopBuildFlags, buildTags } from './release-source.mjs';
import { validateNotices } from './release-notices.mjs';
import { checkBuildMetadata } from './build-info.mjs';
import { runGo } from './go-patches.mjs';

export const assetURL = (tag, name) => `https://github.com/${repository}/releases/download/${encodeURIComponent(tag)}/${encodeURIComponent(name)}`;
export const releaseNames = tag => ({ portable: `still-wanna-dance-${tag}-windows-amd64-portable.zip`, source: `still-wanna-dance-${tag}-corresponding-source.tar.gz`, manifest: `still-wanna-dance-${tag}-release.json`, notes: `still-wanna-dance-${tag}-release-notes.md` });
// Retained only to verify existing artifacts; new releases have no accepted go-lzo risk.
const legacyAcceptedRisk = { module: 'github.com/rasky/go-lzo', version: 'v0.0.0-20200203143853-96a758eda86e', status: 'accepted-nonblocking', acceptedOn: '2026-10-12', evidence: 'source/docs/issue-30-license-audit.md' };
export function renderReleaseNotes({ tag, revision, assets }) {
  const portable = assets.find(asset => asset.kind === 'portable');
  const source = assets.find(asset => asset.kind === 'source');
  const docs = `https://github.com/${repository}/blob/${revision}`;
  return `SWD ${tag}

- [下载 Windows 64 位便携版](${portable.url}) · [SHA256 校验文件](${portable.url}.sha256)
- [完整对应源码](${source.url}) · [SHA256 校验文件](${source.url}.sha256)

解压便携版后，运行 \`SWD/SWD.exe\` 即可。

自行构建请使用上面的完整对应源码附件，具体步骤见[构建说明](${docs}/docs/corresponding-source.md)。

本版本按 GPLv3 分发，历史 MIT 和第三方许可保留于包内 \`licenses/\`。详见[许可与再分发说明](${docs}/LICENSING.md)。

构建提交：[${revision.slice(0, 7)}](https://github.com/${repository}/commit/${revision})
`;
}

const moduleIdentity = modules => JSON.stringify([...modules].map(m => [m.path, m.version, m.sum, m.patchSHA256]).sort((a, b) => a[0].localeCompare(b[0])));

export function checkReleaseManifest(m) {
  if (m.schemaVersion !== 1 || m.kind !== 'formal' || m.repository !== repository || !/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(m.tag) || !/^[a-f0-9]{40}$/.test(m.revision) || m.target !== 'windows/amd64' || !/^go\d+\.\d+/.test(m.toolchain) || (JSON.stringify(m.acceptedRisks) !== '[]' && JSON.stringify(m.acceptedRisks) !== JSON.stringify([legacyAcceptedRisk]))) throw Error('Invalid formal release identity');
  const names = releaseNames(m.tag);
  if (!Array.isArray(m.assets) || m.assets.length !== 2) throw Error('Both Portable and corresponding source are required');
  if (!/^[a-f0-9]{64}$/.test(m.executableSHA256) || !Array.isArray(m.modules) || m.independentRebuild?.verified !== true || m.independentRebuild?.bitReproducible !== false || !/^[a-f0-9]{64}$/.test(m.independentRebuild?.executableSHA256)) throw Error('Missing independent build evidence');
  for (const kind of ['portable', 'source']) {
    const a = m.assets.find(a => a.kind === kind);
    if (!a || a.name !== names[kind] || a.url !== assetURL(m.tag, a.name) || !/^[a-f0-9]{64}$/.test(a.sha256) || !Number.isSafeInteger(a.bytes) || a.bytes < 1) throw Error(`Invalid ${kind} asset`);
  }
  return m;
}

export function checkDelivery(folder, manifest, sourceAsset, sourceRoot, acceptedRisks = []) {
  const info = executableInfo(path.join(folder, 'SWD.exe'));
  checkExecutable(info, manifest);
  validateNotices(sourceRoot, info.modules);
  const expected = readJSON(fileURLToPath(new URL('./portable-files.json', import.meta.url))).sort();
  if (JSON.stringify(filesUnder(folder)) !== JSON.stringify(expected)) throw Error('Unexpected Portable layout or missing legal/source files');
  const record = readJSON(path.join(folder, 'licenses/SOURCE.json'));
  if (record.kind !== 'formal' || record.tag !== manifest.tag || record.revision !== manifest.revision || record.sourceSHA256 !== sourceAsset.sha256 || record.sourceURL !== assetURL(manifest.tag, sourceAsset.name) || record.executableSHA256 !== sha256(path.join(folder, 'SWD.exe')) || JSON.stringify(record.acceptedRisks) !== JSON.stringify(acceptedRisks)) throw Error('Portable/source identity mismatch');
  // Windows packaging/verification also exercises the runtime diagnostic, before any app startup.
  // Other hosts still inspect real EXE VCS/module settings above, without executing foreign code.
  if (process.platform === 'win32') {
    checkBuildMetadata(JSON.parse(run(path.join(folder, 'SWD.exe'), ['-build-info'])), { kind: 'formal', version: manifest.tag, revision: manifest.revision, dirty: false, toolchain: manifest.toolchain, target: manifest.target });
    if (run(path.join(folder, 'SWD.exe'), ['-show-terms']) !== fs.readFileSync(path.join(sourceRoot, 'internal/legal/TERMS.txt'), 'utf8')) throw Error('Embedded terms differ from corresponding source');
  }
  for (const name of ['LICENSE', 'LICENSE-MIT', 'LICENSING.md']) if (sha256(path.join(folder, 'licenses', name)) !== sha256(path.join(sourceRoot, name))) throw Error(`Delivered license differs from source: ${name}`);
  const notices = fs.readFileSync(path.join(folder, 'licenses/THIRD-PARTY-NOTICES.txt'), 'utf8');
  if (!notices.startsWith(fs.readFileSync(path.join(sourceRoot, 'THIRD-PARTY-NOTICES.txt'), 'utf8').trimEnd()) || !notices.includes(`Go runtime and standard library: ${manifest.toolchain}`)) throw Error('Incomplete packaged notices');
  const webuiNotices=path.join(sourceRoot,'docs/licenses/WEBUI-NOTICES.txt');
  if(fs.existsSync(webuiNotices) && !notices.includes(fs.readFileSync(webuiNotices,'utf8').trim()))throw Error('Missing embedded frontend license notices');
  const instructions = fs.readFileSync(path.join(folder, 'licenses/SOURCE.txt'), 'utf8');
  if (!instructions.includes(record.sourceURL) || !instructions.includes(record.sourceSHA256)) throw Error('Missing source download instructions');
  return info;
}

function compareRebuild(original, built) {
  if (moduleIdentity(original.modules) !== moduleIdentity(built.info.modules)) throw Error('Independent rebuild has different binary dependencies');
}

export function createRelease({ root, tag, outputDir, previousZip }) {
  if (process.platform !== 'win32') throw Error('Formal Portable packaging requires Windows');
  root = path.resolve(root); outputDir = path.resolve(outputDir);
  if (previousZip) previousZip = path.resolve(previousZip);
  const revision = cleanRevision(root, tag), names = releaseNames(tag);
  const outputs = [names.portable, names.portable + '.sha256', names.source, names.source + '.sha256', names.notes, names.manifest];
  for (const name of outputs) if (fs.existsSync(path.join(outputDir, name))) throw Error(`Output already exists: ${name}`);
  return withTemp(temp => {
    // A clean checkout excludes ignored local Go files or embedded assets too.
    const checkout = path.join(temp, 'checkout');
    run('git', ['clone', '--quiet', '--no-hardlinks', '--no-checkout', root, checkout], root);
    run('git', ['-c', 'core.autocrlf=false', 'checkout', '--quiet', '--detach', revision], checkout);
    const nodeTests = fs.readdirSync(path.join(checkout, 'scripts')).filter(f => f.endsWith('.test.mjs')).map(f => 'scripts/' + f);
    console.log('Checking clean release checkout...');
    run(process.execPath, ['--test', ...nodeTests], checkout);
    runGo(['test', '-tags=' + buildTags, './cmd/...', './internal/...'], checkout, goEnv());
    runGo(['vet', '-tags=' + buildTags, './cmd/...', './internal/...'], checkout, goEnv());
    const results = path.join(temp, 'results'); fs.mkdirSync(results);
    console.log('Collecting complete corresponding source...');
    const source = createSource({ root: checkout, tag, outputDir: results });
    const bundle = extractSource(source.archive, path.join(temp, 'unpacked'));
    const folder = path.join(temp, 'portable/SWD'), licenses = path.join(folder, 'licenses'); fs.mkdirSync(licenses, { recursive: true });
    const env = goEnv({ GOPROXY: pathToFileURL(path.join(bundle, 'proxy')).href, GOSUMDB: 'off', GONOPROXY: 'none', GONOSUMDB: 'none', GOPRIVATE: '', GOMODCACHE: path.join(temp, 'modules'), GOCACHE: path.join(temp, 'build') });
    console.log('Building pinned desktop executable...');
    runGo(['build', ...desktopBuildFlags({ kind: 'formal', version: tag }), '-buildvcs=true', '-o', path.join(folder, 'SWD.exe'), desktopEntry], checkout, env);
    for (const name of ['LICENSE', 'LICENSE-MIT', 'LICENSING.md']) fs.copyFileSync(path.join(checkout, name), path.join(licenses, name));
    run('pwsh', ['-NoProfile', '-File', path.join(checkout, 'scripts/package-notices.ps1'), '-Executable', path.join(folder, 'SWD.exe'), '-Output', path.join(licenses, 'THIRD-PARTY-NOTICES.txt')], checkout, goEnv());
    const sourceAsset = { kind: 'source', name: names.source, sha256: source.sha256, bytes: fs.statSync(source.archive).size, url: assetURL(tag, names.source) };
    const record = { schemaVersion: 1, kind: 'formal', tag, revision, sourceURL: sourceAsset.url, sourceSHA256: source.sha256, executableSHA256: sha256(path.join(folder, 'SWD.exe')), acceptedRisks: [] };
    writeJSON(path.join(licenses, 'SOURCE.json'), record);
    fs.writeFileSync(path.join(licenses, 'SOURCE.txt'), `Complete Corresponding Source / 完整对应源码\nVersion: ${tag}\nCommit: ${revision}\nDownload: ${sourceAsset.url}\nSHA256: ${source.sha256}\n\nDownload the source asset from the same official GitHub Release as this ZIP.\nThe automatic GitHub Source code ZIP/TAR is not a substitute. Extract the\nsource asset and follow BUILD.txt (recorded Go toolchain, Node.js 22+, pinned pnpm, tar).\nMirrors must retain notices and provide this matching source with equivalent\naccess. For offline redistribution supply the matching source archive too;\na network-only link is not an offline source-delivery plan. See LICENSING.md.\n`);
    const info = checkDelivery(folder, source.manifest, sourceAsset, path.join(bundle, 'source'));
    console.log('Rebuilding independently from delivered source with fresh caches...');
    const built = rebuild({ bundle, output: path.join(temp, 'rebuilt.exe') }); compareRebuild(info, built);
    const zip = path.join(results, names.portable);
    run('pwsh', ['-NoProfile', '-File', path.join(checkout, 'scripts/archive-portable.ps1'), '-Folder', folder, '-Zip', zip], checkout);
    fs.writeFileSync(zip + '.sha256', `${sha256(zip)}  ${names.portable}\n`);
    console.log('Checking actual Portable ZIP in an isolated consent-gated process...');
    // Retain diagnostics in the original repository, outside both temporary roots.
    run('pwsh', ['-NoProfile', '-File', path.join(checkout, 'scripts/test-portable.ps1'), '-Zip', zip, '-EvidenceDir', path.join(root, 'artifacts/portable-smoke')], checkout);
    if (previousZip) run('pwsh', ['-NoProfile', '-File', path.join(checkout, 'scripts/test-portable-upgrade.ps1'), '-PreviousZip', previousZip, '-Zip', zip, '-EvidenceDir', path.join(root, 'artifacts/portable-upgrade')], checkout);
    const portableAsset = { kind: 'portable', name: names.portable, sha256: sha256(zip), bytes: fs.statSync(zip).size, url: assetURL(tag, names.portable) };
    fs.writeFileSync(zip + '.sha256', `${portableAsset.sha256}  ${names.portable}\n`);
    const manifest = { schemaVersion: 1, kind: 'formal', repository, tag, revision, target: source.manifest.target, toolchain: source.manifest.toolchain, executableSHA256: record.executableSHA256, modules: info.modules, acceptedRisks: [], independentRebuild: { verified: true, executableSHA256: built.executableSHA256, bitReproducible: false }, assets: [portableAsset, sourceAsset] };
    checkReleaseManifest(manifest);
    writeJSON(path.join(results, names.manifest), manifest);
    fs.writeFileSync(path.join(results, names.notes), renderReleaseNotes(manifest));
    if (cleanRevision(root, tag) !== revision || cleanRevision(checkout, tag) !== revision) throw Error('Release inputs changed during packaging');
    fs.mkdirSync(outputDir, { recursive: true });
    for (const name of outputs) fs.copyFileSync(path.join(results, name), path.join(outputDir, name), fs.constants.COPYFILE_EXCL);
    return manifest;
  });
}

export function verifyRelease({ manifest, directory }) {
  checkReleaseManifest(manifest); directory = path.resolve(directory);
  for (const a of manifest.assets) {
    const file = path.join(directory, a.name);
    if (fs.statSync(file).size !== a.bytes || sha256(file) !== a.sha256) throw Error(`Release asset checksum mismatch: ${a.name}`);
  }
  return withTemp(temp => {
    const sourceAsset = manifest.assets.find(a => a.kind === 'source');
    const bundle = extractSource(path.join(directory, sourceAsset.name), path.join(temp, 'source'));
    const source = readJSON(path.join(bundle, 'source-manifest.json'));
    if (source.tag !== manifest.tag || source.revision !== manifest.revision || source.toolchain !== manifest.toolchain) throw Error('Release/source version mismatch');
    extractArchive(path.join(directory, manifest.assets.find(a => a.kind === 'portable').name), path.join(temp, 'portable'), 'SWD');
    const folder = path.join(temp, 'portable/SWD');
    const info = checkDelivery(folder, source, sourceAsset, path.join(bundle, 'source'), manifest.acceptedRisks);
    if (sha256(path.join(folder, 'SWD.exe')) !== manifest.executableSHA256 || moduleIdentity(info.modules) !== moduleIdentity(manifest.modules)) throw Error('Release/binary identity mismatch');
    const built = rebuild({ bundle, output: path.join(temp, 'rebuilt.exe') }); compareRebuild(info, built);
    return { tag: manifest.tag, revision: manifest.revision, verified: true, executableSHA256: manifest.executableSHA256 };
  });
}

export async function downloadAssets(manifest, directory, fetcher = fetch) {
  checkReleaseManifest(manifest);
  for (const a of manifest.assets) {
    const response = await fetcher(a.url, { signal: AbortSignal.timeout(600000) });
    if (!response.ok || response.status !== 200 || !response.body) throw Error(`Public ${a.kind} asset is unavailable (HTTP ${response.status})`);
    const file = path.join(directory, a.name);
    await pipeline(Readable.fromWeb(response.body), fs.createWriteStream(file, { flags: 'wx' }));
    if (fs.statSync(file).size !== a.bytes || sha256(file) !== a.sha256) throw Error(`Downloaded ${a.kind} asset differs from release manifest`);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [command, ...rest] = process.argv.slice(2); const a = {};
    for (let i = 0; i < rest.length; i += 2) { if (!rest[i].startsWith('--') || !rest[i + 1]) throw Error('Expected --name value pairs'); a[rest[i].slice(2)] = rest[i + 1]; }
    if (command === 'create') console.log(JSON.stringify(createRelease({ root: a.root ?? '.', tag: a.tag, outputDir: a.output ?? 'artifacts', previousZip: a['previous-portable'] })));
    else if (command === 'verify') console.log(JSON.stringify(verifyRelease({ manifest: readJSON(a.manifest), directory: a.directory ?? path.dirname(path.resolve(a.manifest)) })));
    else if (command === 'verify-published') await withTemp(async temp => { const manifest = readJSON(a.manifest); await downloadAssets(manifest, temp); console.log(JSON.stringify(verifyRelease({ manifest, directory: temp }))); });
    else throw Error('Use create --tag vX.Y.Z [--output artifacts], verify --manifest FILE [--directory DIR], or verify-published --manifest FILE');
  } catch (e) { console.error(e.message); process.exitCode = 1; }
}
