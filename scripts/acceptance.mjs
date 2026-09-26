// Opt-in real-network acceptance. All downloads and reports stay under test-runs.
// node scripts/acceptance.mjs [stepstash-executable]
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import net from 'node:net';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';
import assert from 'node:assert/strict';

const [executable] = process.argv.slice(2);
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
async function stepstash(name, storageDir) {
  const port = await freePort();
  const child = await launch(binary, ['-listen', `127.0.0.1:${port}`,
    '-storage-dir', storageDir, '-download-timeout', '5m'], name, port);
  return { child, port };
}
function videoFile(root, url) {
 const [, id, version] = url.pathname.match(/\/([1-9][0-9]*)-([a-zA-Z0-9]+)\.mp4$/);
 const key = digest(id + '/' + version + '/' + url.searchParams.get('e').toLowerCase() + '/' + Number(url.searchParams.get('s')));
 return path.join(root, 'videos', key + '.mp4');
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
  const songs = path.join(lab, 'storage');
  let service;
  service = await stepstash('cold', songs);
  for (const id of ['1343', '1344']) {
    const url = new URL(urls[id]);
    record(`cold-${id}-started`, { host: url.hostname });
    const res = await request(url.toString(), { port: service.port });
    assert.equal(res.status, 200); assert.equal(res.headers['x-stepstash-cache'], 'MISS');
    assert.equal(res.md5, url.searchParams.get('e')); assert.equal(res.bytes, Number(url.searchParams.get('s')));
    assert.equal(digest(fs.readFileSync(videoFile(songs, url)), 'md5'), res.md5);
    assert.equal(fs.existsSync(path.join(songs, id)), false);
    record(`cold-${id}`, evidence(res));
    url.hostname = url.hostname === 'play.udon.dance' ? 'nya.xin.moe' : 'play.udon.dance';
    const hot = await request(url.toString(), { port: service.port, headers: { Range: 'bytes=0-1023' } });
    assert.equal(hot.status, 206); assert.equal(hot.headers['x-stepstash-cache'], 'HIT');
    record(`cross-host-${id}`, evidence(hot));
  }
  await stop(service.child);
  service = await stepstash('restart', songs);
  for (const id of ['1343', '1344']) {
    const res = await request(urls[id], { port: service.port, method: 'HEAD' });
    assert.equal(res.status, 200); assert.equal(res.headers['x-stepstash-cache'], 'HIT');
    record(`restart-${id}`, evidence(res));
  }
  await stop(service.child);

  assert.equal(fs.readdirSync(path.join(songs, 'videos')).filter(n => n.endsWith('.mp4')).length, 2);
  assert.equal(fs.existsSync(path.join(songs, 'stepstash.sqlite')), true);
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
