// Opt-in real-network acceptance. All downloads and reports stay under test-runs.
// node scripts/acceptance.mjs <existing-song-library> <original-executable> [stepstash-executable]
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import net from 'node:net';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';
import assert from 'node:assert/strict';

const [library, original, executable] = process.argv.slice(2);
if (!library || !original) throw new Error('Supply existing library and original executable paths');
const root = process.cwd();
const binary = path.resolve(executable || path.join(root, 'bin', 'stepstash.exe'));
const lab = path.join(root, 'test-runs', `acceptance-${Date.now()}`);
fs.mkdirSync(lab, { recursive: true });
const report = { started: new Date().toISOString(), binarySHA256: crypto.createHash('sha256').update(fs.readFileSync(binary)).digest('hex'), checks: [] };
const children = new Set();
const digest = (data, algorithm = 'sha256') => crypto.createHash(algorithm).update(data).digest('hex');
const record = (name, fields = {}) => {
  const entry = { name, ...fields };
  report.checks.push(entry);
  fs.writeFileSync(path.join(lab, 'results.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify(entry));
};
const freePort = () => new Promise((resolve, reject) => {
  const server = net.createServer();
  server.once('error', reject);
  server.listen(0, '127.0.0.1', () => { const port = server.address().port; server.close(() => resolve(port)); });
});
function request(target, { port, method = 'GET', headers = {}, timeout = 360000 } = {}) {
  return new Promise((resolve, reject) => {
    const url = new URL(target);
    const started = Date.now();
    let firstByteMs;
    const req = http.request({ hostname: port ? '127.0.0.1' : url.hostname, port: port || url.port || 80,
      path: url.pathname + url.search, method, headers: { Host: url.host, ...headers }, agent: false }, res => {
      firstByteMs = Date.now() - started;
      const sha = crypto.createHash('sha256'), md5 = crypto.createHash('md5');
      let bytes = 0;
      res.on('data', b => { bytes += b.length; sha.update(b); md5.update(b); });
      res.on('error', error => { clearTimeout(deadline); reject(error); });
      res.on('end', () => { clearTimeout(deadline); resolve({ status: res.statusCode, headers: res.headers,
        bytes, sha256: sha.digest('hex'), md5: md5.digest('hex'), firstByteMs, elapsedMs: Date.now() - started }); });
    });
    const deadline = setTimeout(() => req.destroy(new Error('request deadline exceeded')), timeout);
    req.on('error', e => { clearTimeout(deadline); reject(e); });
    req.end();
  });
}
function evidence(result) {
  const headers = Object.fromEntries(Object.entries(result.headers).filter(([k]) =>
    ['content-length', 'content-range', 'content-type', 'etag', 'last-modified', 'x-stepstash-cache'].includes(k)));
  return { ...result, headers };
}
async function launch(executable, args, name, port, env = process.env, cwd = lab) {
  const out = fs.openSync(path.join(lab, `${name}.stdout.log`), 'w');
  const err = fs.openSync(path.join(lab, `${name}.stderr.log`), 'w');
  const child = spawn(executable, args, { windowsHide: true, cwd, env, stdio: ['ignore', out, err] });
  children.add(child);
  fs.closeSync(out); fs.closeSync(err);
  let failure;
  child.on('error', e => { failure = e; });
  for (let i = 0; i < 50; i++) {
    if (failure) throw failure;
    if (child.exitCode !== null) throw new Error(`${name} exited: ${child.exitCode}`);
    try { await request('http://play.udon.dance/', { port, method: 'HEAD', timeout: 500 }); return child; } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`${name} did not become ready`);
}
async function stop(child) {
  if (!child.pid) { children.delete(child); return; }
  if (child.exitCode === null && child.signalCode === null) {
    const exited = new Promise(resolve => child.once('exit', resolve));
    child.kill();
    await exited;
  }
  children.delete(child);
}
async function stepstash(name, songsDir, stateName = name) {
  const port = await freePort();
  const child = await launch(binary, ['-listen', `127.0.0.1:${port}`,
    '-songs-dir', songsDir, '-cache-dir', path.join(lab, stateName), '-download-timeout', '5m'], name, port);
  return { child, port };
}
function snapshot(id) {
  return Object.fromEntries(fs.readdirSync(path.join(library, id)).filter(name => fs.statSync(path.join(library, id, name)).isFile())
    .map(name => [name, digest(fs.readFileSync(path.join(library, id, name)))]));
}

try {
  const urls = {};
  for (const [id, node] of [['1343', 'cf'], ['1344', 'nya']]) {
    const res = await request(`http://api.udon.dance/Api/Songs/play?node=${node}&id=${id}`, { timeout: 20000 });
    assert.equal(res.status, 302);
    const url = new URL(res.headers.location);
    assert.equal(url.protocol, 'http:');
    assert.equal(url.hostname, node === 'cf' ? 'play.udon.dance' : 'nya.xin.moe');
    urls[id] = url.toString();
    record(`api-${node}`, { status: res.status, url: urls[id] });
  }
  const before = { '1343': snapshot('1343'), '1344': snapshot('1344') };
  let service = await stepstash('existing-library', path.resolve(library));
  for (const id of ['1343', '1344']) {
    const content = fs.readFileSync(path.join(library, id, 'video.mp4'));
    const res = await request(urls[id], { port: service.port });
    assert.equal(res.status, 200); assert.equal(res.headers['x-stepstash-cache'], 'HIT');
    assert.equal(res.sha256, digest(content));
    record(`existing-full-${id}`, evidence(res));
    for (const [label, range, start, end] of [['middle', 'bytes=20000000-20001023', 20000000, 20001024],
      ['suffix', 'bytes=-1024', content.length - 1024, content.length]]) {
      const rangeRes = await request(urls[id], { port: service.port, headers: { Range: range } });
      assert.equal(rangeRes.status, 206); assert.equal(rangeRes.sha256, digest(content.subarray(start, end)));
      record(`existing-${label}-${id}`, evidence(rangeRes));
    }
    const head = await request(urls[id], { port: service.port, method: 'HEAD' });
    assert.equal(head.status, 200); assert.equal(head.bytes, 0); assert.equal(Number(head.headers['content-length']), content.length);
    record(`existing-head-${id}`, evidence(head));
  }
  await stop(service.child);
  assert.deepEqual({ '1343': snapshot('1343'), '1344': snapshot('1344') }, before);
  record('existing-library-unchanged', { passed: true, hashes: before });

  const songs = path.join(lab, 'downloaded-songs');
  service = await stepstash('cold', songs);
  for (const id of ['1343', '1344']) {
    const url = new URL(urls[id]);
    record(`cold-${id}-started`, { host: url.hostname });
    const res = await request(url.toString(), { port: service.port });
    assert.equal(res.status, 200); assert.equal(res.headers['x-stepstash-cache'], 'MISS');
    assert.equal(res.md5, url.searchParams.get('e')); assert.equal(res.bytes, Number(url.searchParams.get('s')));
    assert.equal(digest(fs.readFileSync(path.join(songs, id, 'video.mp4')), 'md5'), res.md5);
    assert.equal(JSON.parse(fs.readFileSync(path.join(songs, id, 'metadata.json'))).checksum, res.md5);
    record(`cold-${id}`, evidence(res));
    url.hostname = url.hostname === 'play.udon.dance' ? 'nya.xin.moe' : 'play.udon.dance';
    const hot = await request(url.toString(), { port: service.port, headers: { Range: 'bytes=0-1023' } });
    assert.equal(hot.status, 206); assert.equal(hot.headers['x-stepstash-cache'], 'HIT');
    record(`cross-host-${id}`, evidence(hot));
  }
  await stop(service.child);
  service = await stepstash('restart-no-state', songs);
  for (const id of ['1343', '1344']) {
    const res = await request(urls[id], { port: service.port, method: 'HEAD' });
    assert.equal(res.status, 200); assert.equal(res.headers['x-stepstash-cache'], 'HIT');
    record(`restart-no-state-${id}`, evidence(res));
  }
  await stop(service.child);

  // Reuse the existing installation's consent marker; never infer acceptance.
  const originalRoot = path.dirname(path.resolve(original));
  for (const name of ['LICENSE.txt', 'I_AGREE_TO_THE_LICENSE.txt']) fs.copyFileSync(path.join(originalRoot, name), path.join(lab, name));
  const port = await freePort(), tlsPort = await freePort();
  const originalEnv = { ...process.env, VIDEO_PATH_UD: songs, CACHE_PATH_UD: path.join(lab, 'original-state'),
    LISTEN: `127.0.0.1:${port}`, BUILTIN_SNI_LISTEN: `127.0.0.1:${tlsPort}`, NO_AUTH: 'true', RUST_LOG: 'info' };
  const old = await launch(path.resolve(original), [], 'original-readback', port, originalEnv);
  for (const id of ['1343', '1344']) {
    const res = await request(`http://api.udon.dance/v/${id}`, { port });
    assert.equal(res.status, 200); assert.equal(res.md5, new URL(urls[id]).searchParams.get('e'));
    record(`original-readback-${id}`, evidence(res));
  }
  await stop(old);
  report.passed = true;
} catch (error) {
  report.passed = false;
  report.error = error.stack;
  console.error(error);
  process.exitCode = 1;
} finally {
  for (const child of children) await stop(child);
  report.finished = new Date().toISOString();
  fs.writeFileSync(path.join(lab, 'results.json'), JSON.stringify(report, null, 2));
  console.log(`Report: ${lab}`);
}
