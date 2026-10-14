import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const git = (args, cwd, input) => execFileSync('git', args, { cwd, input, maxBuffer: 256 * 1024 * 1024, windowsHide: true });
const sha256 = data => createHash('sha256').update(data).digest('hex');

export function classify(file, bytes, policy) {
  let binary = bytes.subarray(0, 8000).includes(0);
  try { new TextDecoder('utf-8', { fatal: true }).decode(bytes); } catch { binary = true; }
  const exception = policy.exceptions.find(e => e.path === file && e.sha256 === sha256(bytes));
  if (binary && exception?.kind !== 'binary-fixture') return { error: 'binary content requires an exact fixture exception' };
  if (/\.(?:png|jpe?g|webp|gif|ico|zip|gz|7z|exe|dll|mp4|etl|pdf)$/i.test(file)) return { error: 'generated/binary artifact belongs in an evidence or release archive' };
  if (file.endsWith('.svg') && /<(?:image|script)\b|data\s*:/i.test(bytes.toString())) return { error: 'SVG must be vector-only, without embedded raster or scripts' };
  if (bytes.length > policy.maxBytes && !exception) return { error: 'large file requires an exact documented exception' };
  if (bytes.length > policy.warnBytes && !exception) return { warning: 'review whether this belongs in an archive' };
  return {};
}

export function check({ cwd = root, policy, base, head = 'HEAD' }) {
  policy ??= JSON.parse(readFileSync(path.join(cwd, 'scripts/repository-artifacts-policy.json')));
  base ??= policy.historyBaseline;
  git(['merge-base', '--is-ancestor', base, head], cwd);
  const paths = new Map();
  const add = (oid, file) => { if (!/^0+$/.test(oid)) { const set = paths.get(oid) ?? new Set(); set.add(file); paths.set(oid, set); } };
  // Inspect every added/changed path in every outgoing commit, including files
  // subsequently deleted. Track all paths per blob so renames cannot borrow an
  // unrelated fixture's exception. First-parent merge diffs cover merged files.
  const changes = git(['log', '--raw', '--no-abbrev', '--no-renames', '--diff-merges=first-parent', '--format=', '-z', `${base}..${head}`], cwd).toString('utf8').split('\0');
  for (let i = 0; i < changes.length; i++) {
    const match = changes[i].trim().match(/^:\d+ (\d+) [a-f0-9]+ ([a-f0-9]+) [A-Z]\d*$/);
    if (match) { const name = changes[++i]; if (match[1] !== '160000') add(match[2], name); }
  }
  // Also check the final tree so an inaccurate/empty range cannot skip files.
  for (const row of git(['ls-tree', '-rz', head], cwd).toString('utf8').split('\0')) {
    if (!row) continue;
    const [meta, name] = row.split('\t');
    const [, type, oid] = meta.split(' ');
    if (type === 'blob') add(oid, name);
  }
  const ids = [...paths.keys()];
  const stream = git(['cat-file', '--batch'], cwd, ids.join('\n') + '\n');
  let offset = 0; const errors = [], warnings = [];
  for (const oid of ids) {
    const end = stream.indexOf(10, offset);
    const [actual, type, size] = stream.subarray(offset, end).toString().split(' ');
    if (actual !== oid || type !== 'blob') throw Error('Unexpected Git batch response');
    const bytes = stream.subarray(end + 1, end + 1 + Number(size)); offset = end + 2 + Number(size);
    for (const file of paths.get(oid)) {
      const result = classify(file, bytes, policy);
      if (result.error) errors.push({ path: file, blob: oid, bytes: bytes.length, reason: result.error });
      if (result.warning) warnings.push({ path: file, blob: oid, bytes: bytes.length, reason: result.warning });
    }
  }
  return { base, head: git(['rev-parse', head], cwd).toString().trim(), blobs: ids.length, errors, warnings };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  if (args.some((a, i) => i % 2 === 0 && !['--base', '--head'].includes(a)) || args.length % 2) throw Error('Usage: node scripts/check-repository-artifacts.mjs [--base REF] [--head REF]');
  const options = Object.fromEntries(Array.from({ length: args.length / 2 }, (_, i) => [args[i * 2].slice(2), args[i * 2 + 1]]));
  const result = check(options); console.log(JSON.stringify(result, null, 2));
  if (result.errors.length) process.exitCode = 1;
}
