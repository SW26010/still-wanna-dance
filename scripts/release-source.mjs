// SPDX-License-Identifier: GPL-3.0-only
// Complete module ZIPs preserve upstream source/generator inputs that vendor omits.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { collectFrontend, rebuildFrontend } from './webui-source.mjs';
import { identityFlags } from './build-info.mjs';

export const desktopEntry = './cmd/still-wanna-dance-console';
export const repository = 'SW26010/still-wanna-dance';
export const buildTags = 'no_easytier';
export const productionFlags = ['-tags=' + buildTags, '-trimpath', '-mod=readonly', '-ldflags', '-s -w -H=windowsgui'];
export const desktopBuildFlags = identity => [...productionFlags.slice(0, -1), productionFlags.at(-1) + ' ' + identityFlags(identity)];
export function run(command, args, cwd, env = {}) {
  return execFileSync(command, args, { cwd, env: { ...process.env, ...env }, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, windowsHide: true });
}
export function goEnv(extra = {}) {
  return { GOOS: 'windows', GOARCH: 'amd64', GOAMD64: 'v1', CGO_ENABLED: '0', GOFLAGS: '', GOEXPERIMENT: '', GO111MODULE: 'on', GOWORK: 'off', GOTOOLCHAIN: 'local', GOENV: 'off', ...extra };
}
export function sha256(file) {
  const hash = crypto.createHash('sha256'), buffer = Buffer.alloc(1024 * 1024);
  const fd = fs.openSync(file, 'r');
  try { let bytes; while ((bytes = fs.readSync(fd, buffer, 0, buffer.length, null))) hash.update(buffer.subarray(0, bytes)); }
  finally { fs.closeSync(fd); }
  return hash.digest('hex');
}
export const readJSON = file => JSON.parse(fs.readFileSync(file, 'utf8').replace(/^\uFEFF/, ''));
export const writeJSON = (file, value) => fs.writeFileSync(file, JSON.stringify(value, null, 2) + '\n');

export function jsonStream(text) {
  const values = []; let start = -1, depth = 0, quoted = false, escaped = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) { if (escaped) escaped = false; else if (c === '\\') escaped = true; else if (c === '"') quoted = false; continue; }
    if (c === '"') { quoted = true; continue; }
    if (c === '{') { if (depth++ === 0) start = i; }
    else if (c === '}') { if (--depth === 0) { values.push(JSON.parse(text.slice(start, i + 1))); start = -1; } }
    else if (depth === 0 && !/\s|\uFEFF/.test(c)) throw Error('Invalid Go JSON stream');
  }
  if (depth || quoted || start !== -1) throw Error('Incomplete Go JSON stream');
  return values;
}

export function safePath(root, relative) {
  if (!relative || relative.includes('\\') || relative.includes(':') || /[\x00-\x1f]/.test(relative) || path.posix.isAbsolute(relative) || relative.split('/').some(p => !p || p === '.' || p === '..')) throw Error(`Unsafe path: ${relative}`);
  const full = path.resolve(root, relative);
  if (!full.startsWith(path.resolve(root) + path.sep)) throw Error(`Path escapes root: ${relative}`);
  return full;
}
export function filesUnder(root, dir = root) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap(e => {
    const file = path.join(dir, e.name);
    if (e.isSymbolicLink() || (!e.isFile() && !e.isDirectory())) throw Error(`Unsupported source entry: ${file}`);
    return e.isDirectory() ? filesUnder(root, file) : [path.relative(root, file).split(path.sep).join('/')];
  }).sort();
}
export function withTemp(fn) {
  const base = path.resolve(os.tmpdir());
  const dir = fs.mkdtempSync(path.join(base, 'swd-source-'));
  const cleanup = () => {
    const target = path.resolve(dir);
    if (path.dirname(target) !== base || !path.basename(target).startsWith('swd-source-')) throw Error('Refusing unsafe temporary cleanup');
    fs.rmSync(target, { recursive: true, force: true });
  };
  try { const result = fn(dir); if (result?.then) return result.finally(cleanup); cleanup(); return result; }
  catch (e) { cleanup(); throw e; }
}
export function cleanRevision(root, tag) {
  if (run('git', ['status', '--porcelain', '--untracked-files=all'], root).trim()) throw Error('Source packaging requires a clean tracked and untracked worktree');
  const revision = run('git', ['rev-parse', 'HEAD'], root).trim();
  if (!/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(tag)) throw Error('Expected a version tag such as v1.2.3');
  if (run('git', ['rev-parse', `refs/tags/${tag}^{commit}`], root).trim() !== revision) throw Error('Release tag does not point to HEAD');
  const tree = run('git', ['ls-tree', '-r', revision], root);
  if (/^(120000|160000) /m.test(tree)) throw Error('Source delivery must explicitly handle symlinks or submodules before release');
  return revision;
}
export const escapeModule = text => text.replace(/[A-Z]/g, c => '!' + c.toLowerCase());

export function supplementalInputs(policy, modules) {
  const inputs = [];
  for (const mod of modules) {
    const rule = policy.generatedModules.find(r => r.module === mod.path);
    if (!rule) continue;
    if (rule.version !== mod.version) throw Error(`Generated source input review required: ${mod.path}@${mod.version}`);
    if (rule.blocked) throw Error(`Incomplete corresponding source: ${mod.path}: ${rule.blocked}`);
    inputs.push(...rule.inputs);
  }
  return inputs;
}

export function createSource({ root, tag, outputDir }) {
  root = path.resolve(root); outputDir = path.resolve(outputDir);
  const revision = cleanRevision(root, tag);
  const name = `still-wanna-dance-${tag}-corresponding-source.tar.gz`;
  const archive = path.join(outputDir, name);
  if (fs.existsSync(archive) || fs.existsSync(archive + '.sha256')) throw Error('Source output already exists');
  fs.mkdirSync(outputDir, { recursive: true });
  return withTemp(temp => {
    const bundle = path.join(temp, 'SWD-source'); fs.mkdirSync(bundle);
    const source = path.join(bundle, 'source'); fs.mkdirSync(source);
    const snapshot = path.join(temp, 'snapshot.tar');
    run('git', ['archive', '--format=tar', '--output', snapshot, revision], root);
    run('tar', ['-xf', snapshot, '-C', source], root, { TAR_OPTIONS: '' });
    const frontend = collectFrontend(source, bundle, temp);
    const env = goEnv();
    const graph = jsonStream(run('go', ['list', '-mod=readonly', '-m', '-json', 'all'], source, env));
    if (graph.some(m => m.Replace)) throw Error('Replacement modules require review before source delivery');
    const selected = graph.filter(m => !m.Main).sort((a, b) => a.Path.localeCompare(b.Path));
    const packages = jsonStream(run('go', ['list', '-tags=' + buildTags, '-mod=readonly', '-deps', '-json', desktopEntry], source, env));
    const compiled = new Set(packages.filter(p => p.Module && !p.Module.Main).map(p => p.Module.Path));
    const policyFile = path.join(source, 'docs/licenses/source-inputs.json');
    const policy = compiled.size ? readJSON(policyFile) : { generatedModules: [] };
    const extras = supplementalInputs(policy, selected.filter(m => compiled.has(m.Path)).map(m => ({ path: m.Path, version: m.Version })));
    // Preserve full graph metadata, but supply ZIPs for this production build.
    // Unlinked modules can contain unrelated precompiled artifacts with their
    // own source obligations; do not redistribute them as incidental payload.
    // Explicit metadata queries fetch .mod/.info only, including lazy graph
    // entries. Download source ZIPs only for compiled modules. Work outside the
    // snapshot so queries cannot add unrelated sums to its go.sum.
    const metadata = [], downloaded = [];
    const sourceModules = selected.filter(m => compiled.has(m.Path));
    for (let i = 0; i < selected.length; i += 40) metadata.push(...jsonStream(run('go', ['list', '-m', '-json', ...selected.slice(i, i + 40).map(m => `${m.Path}@${m.Version}`)], temp, env)));
    for (let i = 0; i < sourceModules.length; i += 40) downloaded.push(...jsonStream(run('go', ['mod', 'download', '-json', ...sourceModules.slice(i, i + 40).map(m => `${m.Path}@${m.Version}`)], temp, env)));
    const modules = [];
    for (const m of selected) {
      const base = `proxy/${escapeModule(m.Path)}/@v/${escapeModule(m.Version)}`;
      const sourceIncluded = compiled.has(m.Path);
      const d = (sourceIncluded ? downloaded : metadata).find(d => d.Path === m.Path && d.Version === m.Version);
      if (!d || d.Error || !d.GoMod || sourceIncluded && (!d.Zip || !d.Info || !d.Sum || !d.GoModSum)) throw Error(`Incomplete module source: ${m.Path}@${m.Version}`);
      if (sourceIncluded && (m.Sum && m.Sum !== d.Sum || m.GoModSum && m.GoModSum !== d.GoModSum)) throw Error(`Module checksum changed: ${m.Path}`);
      if (sourceIncluded && d.Dir && filesUnder(d.Dir).some(p => /\.(wasm|syso|a|dll)$/i.test(p)) && !policy.generatedModules.some(r => r.module === m.Path && r.inputs.length)) throw Error(`Embedded binary requires original source review: ${m.Path}`);
      for (const [suffix, original] of [...(sourceIncluded ? [['.zip', d.Zip]] : []), ['.mod', d.GoMod]]) {
        const dest = safePath(bundle, base + suffix); fs.mkdirSync(path.dirname(dest), { recursive: true }); fs.copyFileSync(original, dest);
      }
      fs.writeFileSync(safePath(bundle, path.posix.dirname(base) + '/list'), m.Version + '\n');
      writeJSON(safePath(bundle, base + '.info'), { Version: m.Version, Time: d.Time ?? m.Time });
      // No source hash is invented for an unused module whose ZIP was not
      // downloaded. Every delivered metadata file still has a manifest SHA256.
      modules.push({ path: m.Path, version: m.Version, sum: sourceIncluded ? d.Sum : m.Sum ?? null, goModSum: sourceIncluded ? d.GoModSum : m.GoModSum ?? null, proxyBase: base, sourceIncluded });
    }
    if (extras.length) fs.mkdirSync(path.join(bundle, 'upstream'));
    for (const [i, input] of extras.entries()) {
      const dest = safePath(bundle, 'upstream/' + input.file);
      if (input.kind === 'git') {
        const checkout = path.join(temp, `upstream-${i}`);
        if (!Array.isArray(input.paths) || !input.paths.length) throw Error('Git source input needs an explicit path selection');
        for (const name of input.paths) safePath(checkout, name);
        run('git', ['clone', '--quiet', '--filter=blob:none', '--no-checkout', '--depth', '1', '--branch', input.ref, input.url, checkout], temp);
        if (run('git', ['rev-parse', 'HEAD'], checkout).trim() !== input.revision) throw Error('Upstream source tag changed');
        run('git', ['-c', 'core.autocrlf=false', 'archive', '--format=tar', `--prefix=${input.prefix}`, '--output', dest, input.revision, '--', ...input.paths], checkout);
      } else if (input.kind === 'https') run(process.execPath, [path.join(source, 'scripts/fetch-source-input.mjs'), input.url, dest, input.sha256], source);
      else throw Error('Unsupported source input');
      if (sha256(dest) !== input.sha256) throw Error(`Upstream source hash mismatch: ${input.file}`);
    }
    // Downloading must not silently revise the checked-in build inputs.
    const currentMod = run('git', ['show', `${revision}:go.mod`], root);
    const currentSum = run('git', ['show', `${revision}:go.sum`], root);
    if (fs.readFileSync(path.join(source, 'go.mod'), 'utf8') !== currentMod || fs.readFileSync(path.join(source, 'go.sum'), 'utf8') !== currentSum) throw Error('Module download changed source go.mod/go.sum');
    const toolchain = run('go', ['env', 'GOVERSION'], source, env).trim();
    fs.writeFileSync(path.join(bundle, 'BUILD.txt'), `SWD ${tag}\nCommit: ${revision}\nLicense: GPL-3.0-only for new SWD contributions; see source/LICENSING.md.\n\nInstall ${toolchain}, Node.js 22+, the pnpm version in source/webui/package.json,\nand tar (general-purpose build tools).\nFrom this directory run:\n  node source/scripts/release-source.mjs rebuild --bundle . --output ../SWD-rebuilt.exe\nThe verifier checks all source/proxy file hashes, uses fresh Go caches and ONLY\nthe included file GOPROXY; it does not download modules or a toolchain.\nSource includes full module ZIPs, locked npm packages in frontend-store,\noriginal notices, generated inputs and scripts. The frontend is rebuilt offline\nwith pnpm and its assets/notices are byte-checked before the Go build.\nGit export-ignore omits historical experiments/ and docs/evidence/; these remain\nin the repository at the recorded commit. Research/chart inputs are retained.\nNo bit-for-bit binary reproducibility is claimed.\n`);
    const manifest = { schemaVersion: 1, tag, revision, repository, target: 'windows/amd64', toolchain, entry: desktopEntry, productionFlags, modules, supplementalInputs: extras, frontend,
      files: filesUnder(bundle).map(p => ({ path: p, bytes: fs.statSync(safePath(bundle, p)).size, sha256: sha256(safePath(bundle, p)) })) };
    writeJSON(path.join(bundle, 'source-manifest.json'), manifest);
    verifyBundle(bundle);
    if (cleanRevision(root, tag) !== revision) throw Error('Source changed during packaging');
    const partial = path.join(temp, name);
    run('tar', ['-czf', partial, '-C', temp, 'SWD-source'], root, { TAR_OPTIONS: '' });
    fs.copyFileSync(partial, archive, fs.constants.COPYFILE_EXCL);
    const hash = sha256(archive); fs.writeFileSync(archive + '.sha256', `${hash}  ${name}\n`);
    return { archive, sha256: hash, manifest };
  });
}

export function verifyBundle(bundle) {
  const m = readJSON(path.join(bundle, 'source-manifest.json'));
  if (m.schemaVersion !== 1 || !/^[a-f0-9]{40}$/.test(m.revision) || !/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(m.tag) || !/^go\d+\.\d+/.test(m.toolchain) || m.repository !== repository || m.entry !== desktopEntry || m.target !== 'windows/amd64' || JSON.stringify(m.productionFlags) !== JSON.stringify(productionFlags)) throw Error('Invalid corresponding-source identity');
  const seen = new Set();
  for (const f of m.files) {
    const full = safePath(bundle, f.path);
    if (seen.has(f.path) || !fs.lstatSync(full).isFile() || fs.statSync(full).size !== f.bytes || sha256(full) !== f.sha256) throw Error(`Source hash or entry mismatch: ${f.path}`);
    seen.add(f.path);
  }
  for (const name of ['go.mod', 'go.sum', 'LICENSE', 'LICENSE-MIT', 'LICENSING.md', 'THIRD-PARTY-NOTICES.txt', 'scripts/build-desktop.ps1', 'scripts/build-desktop.mjs', 'scripts/build-info.mjs', 'scripts/release-source.mjs']) if (!seen.has('source/' + name)) throw Error(`Missing source input: ${name}`);
  if (!seen.has('BUILD.txt')) throw Error('Missing build instructions');
  if(seen.has('source/webui/package.json')) {
    const pkg=readJSON(path.join(bundle,'source/webui/package.json'));
    if(m.frontend?.packageManager!==pkg.packageManager || m.frontend?.store!=='frontend-store' || ![...seen].some(p=>p.startsWith('frontend-store/')))throw Error('Missing locked frontend source store');
    if(![...seen].some(p=>p.startsWith('frontend-store/registry-cache/v11/metadata-full/')))throw Error('Missing frontend registry metadata for offline policy verification');
    if(seen.has('frontend-store/registry-cache/lockfile-verified.jsonl'))throw Error('Frontend source must not include cached policy verdicts');
    for(const name of ['build-manifest.json','pnpm-lock.yaml','build.mjs','src/main.tsx'])if(!seen.has('source/webui/'+name))throw Error('Missing frontend build input: '+name);
  }
  const modules = new Set();
  for (const mod of m.modules) {
    const id = `${mod.path}@${mod.version}`;
    const base = `proxy/${escapeModule(mod.path)}/@v/${escapeModule(mod.version)}`;
    if (modules.has(id) || mod.proxyBase !== base || [mod.sum, mod.goModSum].some(s => s !== null && !/^h1:/.test(s)) || mod.sourceIncluded && (!mod.sum || !mod.goModSum)) throw Error(`Invalid module identity: ${id}`);
    modules.add(id);
    if (typeof mod.sourceIncluded !== 'boolean') throw Error(`Missing module source scope: ${id}`);
    if (!mod.sourceIncluded && seen.has(base + '.zip')) throw Error(`Unexpected uncompiled module ZIP: ${id}`);
    for (const suffix of [...(mod.sourceIncluded ? ['.zip'] : []), '.mod', '.info']) if (!seen.has(base + suffix)) throw Error(`Missing complete module source: ${id}${suffix}`);
  }
  const compiled = m.modules.filter(mod => mod.sourceIncluded);
  const policy = compiled.length ? readJSON(path.join(bundle, 'source/docs/licenses/source-inputs.json')) : { generatedModules: [] };
  const extras = supplementalInputs(policy, compiled);
  if (JSON.stringify(extras) !== JSON.stringify(m.supplementalInputs)) throw Error('Supplemental source inventory mismatch');
  for (const input of extras) if (!seen.has('upstream/' + input.file) || sha256(safePath(bundle, 'upstream/' + input.file)) !== input.sha256) throw Error(`Missing original source input: ${input.file}`);
  const actual = filesUnder(bundle).filter(p => p !== 'source-manifest.json');
  if (actual.length !== seen.size || actual.some(p => !seen.has(p))) throw Error('Unexpected unmanifested source input');
  return m;
}

export function extractSource(archive, dest) {
  extractArchive(archive, dest, 'SWD-source');
  const bundle = path.join(dest, 'SWD-source'); verifyBundle(bundle); return bundle;
}

export function extractArchive(archive, dest, prefix) {
  // Reject traversal, links and non-regular entries before invoking standard tar.
  const names = run('tar', ['-tf', path.resolve(archive)], undefined, { TAR_OPTIONS: '' }).trim().split(/\r?\n/);
  const modes = run('tar', ['-tvf', path.resolve(archive)], undefined, { TAR_OPTIONS: '' }).trim().split(/\r?\n/);
  const seen = new Set();
  for (const name of names) {
    const p = name.replace(/\/$/, ''); safePath(dest, p);
    if (!(p === prefix || p.startsWith(prefix + '/')) || seen.has(p)) throw Error('Unexpected archive entry');
    seen.add(p);
  }
  if (modes.length !== names.length || modes.some(line => !/^[-d]/.test(line))) throw Error('Source archive contains links or special entries');
  if (fs.existsSync(dest)) throw Error('Extraction destination must be new');
  fs.mkdirSync(dest, { recursive: true });
  run('tar', ['-xf', path.resolve(archive), '-C', dest], undefined, { TAR_OPTIONS: '' });
}

export function executableInfo(executable) {
  const text = run('go', ['version', '-m', path.resolve(executable)]);
  const modules = []; const settings = {};
  let entry, toolchain;
  for (const [i, line] of text.split(/\r?\n/).entries()) {
    if (i === 0) toolchain = line.slice(line.lastIndexOf(': ') + 2);
    const fields = line.trim().split(/\s+/);
    if (fields[0] === '=>') throw Error('Replacement modules are not approved');
    if (fields[0] === 'path') entry = fields[1];
    if (fields[0] === 'dep') modules.push({ path: fields[1], version: fields[2], sum: fields[3] });
    if (fields[0] === 'build') { const kv = fields.slice(1).join(' '); const at = kv.indexOf('='); settings[kv.slice(0, at)] = kv.slice(at + 1); }
  }
  return { entry, toolchain, modules, settings };
}
export function checkExecutable(info, manifest, requireRevision = true) {
  if (info.entry !== 'still-wanna-dance/' + desktopEntry.slice(2) || info.toolchain !== manifest.toolchain || info.settings.GOOS !== 'windows' || info.settings.GOARCH !== 'amd64' || info.settings.GOAMD64 !== 'v1' || info.settings.CGO_ENABLED !== '0' || info.settings['-trimpath'] !== 'true' || info.settings['-tags'] !== buildTags) throw Error('Binary target/toolchain/entry differs from source');
  if (info.modules.some(m => m.path === 'github.com/easytier/easytier/easytier-go')) throw Error('EasyTier must be excluded from the desktop build');
  if (requireRevision && (info.settings['vcs.revision'] !== manifest.revision || info.settings['vcs.modified'] !== 'false')) throw Error('Binary revision is missing, dirty or differs from source');
  for (const mod of info.modules) if (!manifest.modules.some(m => m.path === mod.path && m.version === mod.version && m.sum === mod.sum && m.sourceIncluded)) throw Error(`Binary dependency differs from source: ${mod.path}`);
}

export function rebuild({ bundle, output }) {
  bundle = path.resolve(bundle); output = path.resolve(output);
  const manifest = verifyBundle(bundle);
  if (fs.existsSync(output)) throw Error('Rebuild output already exists');
  if (output.startsWith(bundle + path.sep)) throw Error('Rebuild output must be outside the source bundle');
  fs.mkdirSync(path.dirname(output), { recursive: true });
  return withTemp(temp => {
    const env = goEnv({ GOPROXY: pathToFileURL(path.join(bundle, 'proxy')).href, GOSUMDB: 'off', GONOPROXY: 'none', GONOSUMDB: 'none', GOPRIVATE: '', GOMODCACHE: path.join(temp, 'modules'), GOCACHE: path.join(temp, 'build') });
    const source = path.join(bundle, 'source');
    rebuildFrontend(source,bundle,temp);
    if (run('go', ['env', 'GOVERSION'], source, env).trim() !== manifest.toolchain) throw Error(`Install recorded toolchain ${manifest.toolchain} for independent verification`);
    run('go', ['build', ...desktopBuildFlags({ kind: 'rebuild', version: manifest.tag, revision: manifest.revision }), '-buildvcs=false', '-o', output, desktopEntry], source, env);
    const info = executableInfo(output); checkExecutable(info, manifest, false);
    verifyBundle(bundle); // readonly build must not revise supplied inputs
    return { manifest, info, executableSHA256: sha256(output) };
  });
}

function args(values) {
  const out = {}; for (let i = 0; i < values.length; i += 2) { if (!values[i]?.startsWith('--') || !values[i + 1]) throw Error('Expected --name value pairs'); out[values[i].slice(2)] = values[i + 1]; } return out;
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [command, ...rest] = process.argv.slice(2); const a = args(rest);
    if (command === 'create') { const r = createSource({ root: a.root ?? '.', tag: a.tag, outputDir: a.output ?? 'artifacts' }); console.log(JSON.stringify({ archive: r.archive, sha256: r.sha256 })); }
    else if (command === 'rebuild') { const r = rebuild({ bundle: a.bundle, output: a.output }); console.log(JSON.stringify({ revision: r.manifest.revision, executableSHA256: r.executableSHA256 })); }
    else throw Error('Use create --tag vX.Y.Z [--root .] [--output artifacts], or rebuild --bundle DIR --output EXE');
  } catch (e) { console.error(e.message); process.exitCode = 1; }
}
