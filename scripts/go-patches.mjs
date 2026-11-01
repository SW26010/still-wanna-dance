// SPDX-License-Identifier: GPL-3.0-only
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

export const mihomoPatch = {
  module: 'github.com/metacubex/mihomo', version: 'v1.19.32',
  source: 'transport/anytls/session/stream.go',
  originalSHA256: 'afdb9642846ca669c584b9b317bf4210074cce933db8ed74f02373933630c6e3',
  originalSum: 'h1:uD7ZC3P77isWD554NNvtee65L+99+/C5hyc+Lk8rVEk=',
  replacement: 'patches/mihomo-anytls-stream.go.txt',
};

export function patchManifest(root, modules) {
  const module = modules.find(m => m.path === mihomoPatch.module);
  if (!module) return [];
  if (module.version !== mihomoPatch.version || module.sum !== mihomoPatch.originalSum) throw Error('Mihomo patch base differs from delivered module');
  return [{ ...mihomoPatch, sha256: crypto.createHash('sha256').update(fs.readFileSync(path.join(root, mihomoPatch.replacement))).digest('hex') }];
}

export function readPatchedModule(module, fields) {
  const match = fields[1]?.match(/^(?:\.\.?\/|\/|[A-Za-z]:\/)(?:.*\/)?swd-go-[A-Za-z0-9]{6}\/([a-f0-9]{64})$/);
  if (!module || module.path !== mihomoPatch.module || module.version !== mihomoPatch.version || module.sum || module.patchSHA256 || fields[2] !== '(devel)' || !match) throw Error('Replacement modules are not approved');
  module.patchSHA256 = match[1];
}

export function runGo(args, root = process.cwd(), extraEnv = {}, options = {}) {
  const env = { ...process.env, ...extraEnv };
  const invoke = (values, overrides = {}) => execFileSync('go', values, {
    cwd: root, env, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, windowsHide: true, ...overrides,
  });
  if (/(?:^|\s|["'])-(?:overlay|modfile)(?:=|\s)/.test(env.GOFLAGS ?? '') || args.some(a => /^-(overlay|modfile)(=|$)/.test(a))) throw Error('Use only the reviewed repository Go patch inputs');
  // Small source-delivery fixtures have no Mihomo dependency.
  const modfileInfo = JSON.parse(invoke(['mod', 'edit', '-json']));
  if (!modfileInfo.Require?.some(m => m.Path === mihomoPatch.module)) return invoke(args, options);
  // Query only the dependency we patch. "all" loads unrelated historical graph
  // metadata that a production-only offline source bundle does not need.
  const selected = invoke(['list', '-mod=readonly', '-m', '-f', '{{.Version}}{{if .Replace}} replacement{{end}}', mihomoPatch.module]).trim();
  if (selected !== mihomoPatch.version) throw Error('Mihomo version/replacement changed; review the stream patch before building');
  const module = JSON.parse(invoke(['mod', 'download', '-json', `${mihomoPatch.module}@${selected}`]));
  if (module.Error || !module.Dir || module.Sum !== mihomoPatch.originalSum) throw Error('Pinned Mihomo source is unavailable or its checksum changed');
  const original = path.join(module.Dir, mihomoPatch.source);
  if (crypto.createHash('sha256').update(fs.readFileSync(original)).digest('hex') !== mihomoPatch.originalSHA256) throw Error('Mihomo stream source hash changed; refusing to apply patch');
  const replacement = path.resolve(root, mihomoPatch.replacement);
  if (!fs.statSync(replacement).isFile()) throw Error('Missing reviewed Mihomo stream replacement');
  const hash = crypto.createHash('sha256').update(fs.readFileSync(replacement)).digest('hex');
  // Keep the copy outside nested release/source checkouts: Windows cannot start
  // vet.exe with a package working directory longer than MAX_PATH.
  const base = path.resolve(os.tmpdir());
  fs.mkdirSync(base, { recursive: true });
  const temp = fs.mkdtempSync(path.join(base, 'swd-go-'));
  try {
    const patched = path.join(temp, hash);
    fs.cpSync(module.Dir, patched, { recursive: true });
    // Module-cache directories can be read-only. Only this private copy changes.
    const writableDirectories = dir => {
      fs.chmodSync(dir, 0o755);
      for (const entry of fs.readdirSync(dir, { withFileTypes: true })) if (entry.isDirectory()) writableDirectories(path.join(dir, entry.name));
    };
    writableDirectories(patched);
    const target = path.join(patched, mihomoPatch.source);
    fs.chmodSync(target, 0o644); fs.copyFileSync(replacement, target);
    const modfile = path.join(temp, 'patched.mod');
    const relative = path.relative(root, patched);
    const modulePath = (path.isAbsolute(relative) ? relative : './' + relative).replaceAll('\\', '/');
    fs.writeFileSync(modfile, fs.readFileSync(path.join(root, 'go.mod'), 'utf8') + `\nreplace ${mihomoPatch.module} ${mihomoPatch.version} => ${JSON.stringify(modulePath)}\n`);
    fs.copyFileSync(path.join(root, 'go.sum'), path.join(temp, 'patched.sum'));
    // Inherited by fixture Go builds too. Original go.mod/go.sum and module
    // caches stay intact; the binary explicitly records the patched dependency.
    env.GOFLAGS = `${env.GOFLAGS ?? ''} "-modfile=${modfile.replaceAll('\\', '/')}"`.trim();
    return invoke(args, options);
  } finally {
    if (path.dirname(temp) !== base || !path.basename(temp).startsWith('swd-go-')) throw Error('Unsafe Go patch cleanup path');
    fs.rmSync(temp, { recursive: true, force: true });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { runGo(process.argv.slice(2), process.cwd(), {}, { stdio: 'inherit' }); }
  catch (error) { console.error(error.message); process.exitCode = Number.isInteger(error.status) ? error.status : 1; }
}
