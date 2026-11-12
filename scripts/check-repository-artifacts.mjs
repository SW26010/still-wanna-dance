import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const git = (args, cwd, input, maxBuffer = 256 * 1024 * 1024) => execFileSync('git', args, { cwd, input, maxBuffer, windowsHide: true });
const sha256 = data => createHash('sha256').update(data).digest('hex');
const webuiApp = 'internal/console/assets/webui/app.js';
const largeFileError = 'large file requires an exact documented exception';

export function classify(file, bytes, policy) {
  let binary = bytes.subarray(0, 8000).includes(0);
  try { new TextDecoder('utf-8', { fatal: true }).decode(bytes); } catch { binary = true; }
  const exception = policy.exceptions.find(e => e.path === file && e.sha256 === sha256(bytes));
  const productIcon = exception?.kind === 'product-icon' && /^internal\/branding\/logo(?:(?:-light)?\.ico|_windows_amd64\.syso)$/.test(file) && exception.bytes === bytes.length && bytes.length <= policy.maxBytes;
  const readmeScreenshot = exception?.kind === 'readme-screenshot' && /^docs\/images\/webui-(?:acceleration|library|settings-dark)\.png$/.test(file) && exception.bytes === bytes.length && bytes.length <= policy.warnBytes;
  const categoryCover = exception?.kind === 'category-cover' && /^internal\/console\/assets\/webui\/category-covers\/[a-z0-9-]+\.webp$/.test(file) && exception.bytes === bytes.length && bytes.length <= policy.warnBytes && typeof exception.reason === 'string' && exception.reason.trim().length > 0;
  const dashboard = exception?.kind === 'embedded-dashboard' && file === 'internal/dashboard/assets/zashboard.zip' && exception.bytes === bytes.length;
  const largeText = exception?.kind === 'large-text' && exception.bytes === bytes.length && typeof exception.reason === 'string' && exception.reason.trim().length > 0;
  if (binary && exception?.kind !== 'binary-fixture' && !productIcon && !readmeScreenshot && !dashboard && !categoryCover) return { error: 'binary content requires an exact documented product or fixture exception' };
  if (/\.(?:png|jpe?g|webp|gif|ico|zip|gz|7z|exe|dll|mp4|etl|pdf)$/i.test(file) && !productIcon && !readmeScreenshot && !dashboard && !categoryCover) return { error: 'generated/binary artifact belongs in an evidence or release archive' };
  if (file.endsWith('.svg') && /<(?:image|script)\b|data\s*:/i.test(bytes.toString())) return { error: 'SVG must be vector-only, without embedded raster or scripts' };
  const allowed = largeText || productIcon || readmeScreenshot || dashboard || categoryCover || (binary && exception?.kind === 'binary-fixture');
  if (bytes.length > policy.maxBytes && !allowed) return { error: largeFileError };
  if (bytes.length > policy.warnBytes && !allowed) return { warning: 'review whether this belongs in an archive' };
  return {};
}

// The app.js hash does not validate other manifest fields, authorize an exception or prove a rebuild.
// Callers supply Git-blob bytes (the build caller normalizes its generated text to LF).
function reviewWebuiArtifact(bytes, policy, sources) {
  if (classify(webuiApp, bytes, policy).error !== largeFileError) return null;
  const digest = sha256(bytes);
  const manifests = sources.map(({ source, manifest }) => {
    const expected = manifest?.schemaVersion === 1 ? manifest.outputs?.[webuiApp] : undefined;
    if (manifest === null) return { source, status: 'missing' };
    if (typeof expected !== 'string' || !/^[a-f0-9]{64}$/.test(expected)) return { source, status: 'invalid' };
    return expected === digest ? { source, status: 'matches' } : { source, status: 'mismatch', manifestSha256: expected };
  });
  const review = { sha256: digest, bytes: bytes.length, manifests };
  if (!manifests.length || manifests.some(m => m.status !== 'matches')) return { ...review, status: 'manifest-review-required' };
  if (policy.exceptions.some(e => e.path === webuiApp && e.sha256 === digest)) return { ...review, status: 'existing-exception-needs-correction' };
  return { ...review, status: 'candidate', exception: { path: webuiApp, kind: 'large-text', bytes: bytes.length, sha256: digest, reason: '' } };
}

export function reviewWebuiBuild(contents, policy, manifest) {
  const text = new TextDecoder('utf-8', { fatal: true }).decode(contents);
  return reviewWebuiArtifact(Buffer.from(text.replaceAll('\r\n', '\n')), policy, [{ source: 'working tree', manifest }]);
}

function historicalWebuiManifests(cwd, commits) {
  return [...commits].map(commit => {
    const manifestPath = 'webui/build-manifest.json';
    if (!git(['ls-tree', '-z', commit, '--', manifestPath], cwd).length) return { source: commit, manifest: null };
    const contents = git(['show', `${commit}:${manifestPath}`], cwd);
    try { return { source: commit, manifest: JSON.parse(contents) }; }
    catch { return { source: commit, manifest: {} }; }
  });
}

export function check({ cwd = root, policy, base, head = 'HEAD' }) {
  policy ??= JSON.parse(readFileSync(path.join(cwd, 'scripts/repository-artifacts-policy.json')));
  base ??= policy.historyBaseline;
  git(['merge-base', '--is-ancestor', base, head], cwd);
  const headCommit = git(['rev-parse', head], cwd).toString().trim();
  const paths = new Map();
  const webuiCommits = new Map();
  const add = (oid, file, commit) => {
    if (/^0+$/.test(oid)) return;
    const set = paths.get(oid) ?? new Set(); set.add(file); paths.set(oid, set);
    if (file === webuiApp) { const commits = webuiCommits.get(oid) ?? new Set(); commits.add(commit); webuiCommits.set(oid, commits); }
  };
  // Inspect every added/changed path in every outgoing commit, including files
  // subsequently deleted. Track all paths per blob so renames cannot borrow an
  // unrelated fixture's exception. First-parent merge diffs cover merged files.
  const changes = git(['log', '--raw', '--no-abbrev', '--no-renames', '--diff-merges=first-parent', '--format=commit:%H', '-z', `${base}..${head}`], cwd).toString('utf8').split('\0');
  let commit;
  for (let i = 0; i < changes.length; i++) {
    const revision = changes[i].match(/^commit:([a-f0-9]+)$/);
    if (revision) { commit = revision[1]; continue; }
    const match = changes[i].trim().match(/^:\d+ (\d+) [a-f0-9]+ ([a-f0-9]+) [A-Z]\d*$/);
    if (match) { const name = changes[++i]; if (match[1] !== '160000') add(match[2], name, commit); }
  }
  // Also check the final tree so an inaccurate/empty range cannot skip files.
  for (const row of git(['ls-tree', '-rz', head], cwd).toString('utf8').split('\0')) {
    if (!row) continue;
    const [meta, name] = row.split('\t');
    const [, type, oid] = meta.split(' ');
    if (type === 'blob') add(oid, name, headCommit);
  }
  const ids = [...paths.keys()];
  const errors = [], warnings = [];
  let batch = [], batchBytes = 0;
  const inspectBatch = () => {
    if (!batch.length) return;
    const stream = git(['cat-file', '--batch'], cwd, batch.join('\n') + '\n', batchBytes);
    let offset = 0;
    for (const oid of batch) {
      const end = stream.indexOf(10, offset);
      const [actual, type, size] = stream.subarray(offset, end).toString().split(' ');
      if (actual !== oid || type !== 'blob') throw Error('Unexpected Git batch response');
      const bytes = stream.subarray(end + 1, end + 1 + Number(size)); offset = end + 2 + Number(size);
      for (const file of paths.get(oid)) {
        const result = classify(file, bytes, policy);
        if (result.error) {
          const error = { path: file, blob: oid, bytes: bytes.length, reason: result.error };
          if (file === webuiApp && result.error === largeFileError) error.review = reviewWebuiArtifact(bytes, policy, historicalWebuiManifests(cwd, webuiCommits.get(oid)));
          errors.push(error);
        }
        if (result.warning) warnings.push({ path: file, blob: oid, bytes: bytes.length, reason: result.warning });
      }
    }
    batch = []; batchBytes = 0;
  };
  // Bound each read by bytes, not blob count: history grows with every generated
  // bundle. A single larger blob gets its own batch and is still classified.
  if (ids.length) {
    const headers = git(['cat-file', '--batch-check'], cwd, ids.join('\n') + '\n').toString().trim().split('\n');
    if (headers.length !== ids.length) throw Error('Unexpected Git batch metadata');
    for (const [index, header] of headers.entries()) {
      const [oid, type, size] = header.split(' ');
      if (oid !== ids[index] || type !== 'blob' || !Number.isSafeInteger(Number(size)) || Number(size) < 0) throw Error('Unexpected Git batch metadata');
      const responseBytes = Buffer.byteLength(header) + Number(size) + 2;
      if (batchBytes + responseBytes > 16 * 1024 * 1024) inspectBatch();
      batch.push(oid); batchBytes += responseBytes;
    }
    inspectBatch();
  }
  return { base, head: headCommit, blobs: ids.length, errors, warnings };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  if (args.some((a, i) => i % 2 === 0 && !['--base', '--head'].includes(a)) || args.length % 2) throw Error('Usage: node scripts/check-repository-artifacts.mjs [--base REF] [--head REF]');
  const options = Object.fromEntries(Array.from({ length: args.length / 2 }, (_, i) => [args[i * 2].slice(2), args[i * 2 + 1]]));
  const result = check(options); console.log(JSON.stringify(result, null, 2));
  if (result.errors.length) process.exitCode = 1;
}
