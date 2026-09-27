// Opt-in real-network acceptance. All downloads and reports stay under test-runs.
// node scripts/acceptance.mjs [stepstash-executable] [console-executable]
import fs from 'node:fs';
import path from 'node:path';
import net from 'node:net';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';
import assert from 'node:assert/strict';
import { request } from './acceptance-request.mjs';
import { AcceptanceBlocked, videoRedirect } from './acceptance-protocol.mjs';

const [executable, consoleExecutable] = process.argv.slice(2);
const root = process.cwd();
const binary = path.resolve(executable || path.join(root, 'bin', 'stepstash.exe'));
const consoleBinary = path.resolve(consoleExecutable || path.join(root, 'bin', 'stepstash-console.exe'));
const lab = path.join(root, 'test-runs', `acceptance-${Date.now()}`);
fs.mkdirSync(lab, { recursive: true });
const report = { started: new Date().toISOString(), checks: [] };
const children = new Set();
const servers = new Set();
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

function evidence(result) {
  const headers = Object.fromEntries(Object.entries(result.headers).filter(([k]) =>
    ['location', 'content-length', 'content-range', 'content-type', 'etag', 'last-modified', 'x-stepstash-cache', 'x-stepstash-fallback'].includes(k)));
  return { ...result, headers };
}
async function launch(executable, args, name, port, readyURL = 'http://play.udon.dance/') {
  const out = fs.openSync(path.join(lab, `${name}.stdout.log`), 'w');
  const err = fs.openSync(path.join(lab, `${name}.stderr.log`), 'w');
  const child = spawn(executable, args, { windowsHide: true, cwd: lab, env: process.env, stdio: ['ignore', out, err] });
  children.add(child);
  fs.closeSync(out); fs.closeSync(err);
  let failure;
  child.on('error', e => { failure = e; });
  for (let i = 0; i < 50; i++) {
    if (failure) throw failure;
    if (child.exitCode !== null) throw new Error(`${name} exited: ${child.exitCode}`);
    try { await request(readyURL, { port, method: 'HEAD', timeout: 500 }); return child; } catch {}
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

const playbackURL = (id, node = '') => `http://api.udon.dance/Api/Songs/play?id=${id}${node ? `&node=${node}` : ''}`;
async function check(name, action) {
  try {
    await action();
    record(name, { outcome: 'passed' });
    return true;
  } catch (error) {
    record(name, { outcome: error.category ? 'blocked' : 'failed',
      category: error.category || 'program-failure', error: error.stack });
    return false;
  }
}
async function upstream(id, node) {
  let target = playbackURL(id, node);
  for (let hop = 0; hop < 4; hop++) {
    let res;
    try { res = await request(target, { timeout: 20000 }); }
    catch (error) { throw new AcceptanceBlocked('upstream-unavailable', error.message); }
    record(`upstream-${id}-${node}-${hop}`, { url: target, ...evidence(res) });
    if (res.status >= 500 || [408, 429].includes(res.status)) {
      throw new AcceptanceBlocked('upstream-unavailable', `API status ${res.status}`);
    }
    // HTTP may now redirect to the same API over HTTPS before returning a video.
    let next;
    try { next = new URL(res.headers.location, target); } catch {}
    if ([301, 302, 307, 308].includes(res.status) && next?.protocol === 'https:' &&
        next.hostname === 'api.udon.dance' && next.pathname === '/Api/Songs/play' &&
        !next.username && !next.password && !next.port && !next.hash) {
      target = next.toString();
      continue;
    }
    return videoRedirect(res.status, res.headers.location);
  }
  throw new AcceptanceBlocked('upstream-protocol-change', 'API redirect limit exceeded');
}
function fullVideo(res, url, cache) {
  assert.equal(res.status, 200);
  assert.equal(res.headers['x-stepstash-cache'], cache);
  assert.equal(res.headers.location, undefined, 'local playback must return video, not redirect');
  assert.equal(res.md5, url.searchParams.get('e').toLowerCase());
  assert.equal(res.bytes, Number(url.searchParams.get('s')));
}
function rangeVideo(res, file) {
  const data = fs.readFileSync(file);
  assert.equal(res.status, 206);
  assert.equal(res.headers['x-stepstash-cache'], 'HIT');
  assert.equal(res.headers['content-range'], `bytes 0-1023/${data.length}`);
  assert.equal(res.bytes, 1024);
  assert.equal(res.sha256, digest(data.subarray(0, 1024)));
}
async function consoleService(name, storageDir, extra = {}) {
  const port = await freePort();
  const config = path.join(lab, `${name}.json`);
  fs.writeFileSync(config, JSON.stringify({ storageDir, logDir: path.join(lab, 'game-logs'),
    downloadUpstream: 'auto', autoStartCDN: true, ...extra }));
  const base = `http://127.0.0.1:${port}`;
  const child = await launch(consoleBinary, ['-no-tray', '-no-open', '-listen', `127.0.0.1:${port}`,
    '-config', config], name, port, base);
  const res = await request(`${base}/api/status`, { capture: true, timeout: 10000 });
  assert.equal(res.status, 200);
  const state = JSON.parse(res.body);
  assert.equal(state.running, true, state.cdnError);
  assert.equal(state.settings.downloadUpstream, 'auto');
  record(`${name}-settings`, { downloadUpstream: state.settings.downloadUpstream, running: state.running });
  return { child, port: 80 };
}
async function consolePorts() {
  // Check without stopping any existing service or changing hosts.
  for (const port of [80, 443]) {
    const server = net.createServer();
    try {
      await new Promise((resolve, reject) => {
        server.once('error', reject); server.listen(port, '127.0.0.1', resolve);
      });
    } catch (error) {
      throw new AcceptanceBlocked('environment', `Console needs free loopback port ${port}: ${error.message}`);
    } finally { if (server.listening) await new Promise(resolve => server.close(resolve)); }
  }
}

function requireBinary(name, file) {
  const relative = path.relative(root, file);
  // External binaries retain only their filename; never publish machine paths.
  const reportPath = (path.isAbsolute(relative) || relative === '..' || relative.startsWith(`..${path.sep}`)
    ? path.basename(file) : relative).split(path.sep).join('/');
  if (!fs.existsSync(file)) throw new AcceptanceBlocked('environment', `Missing ${name} binary: ${reportPath}`);
  record('binary', { component: name, path: reportPath, sha256: digest(fs.readFileSync(file)) });
}

try {
  await check('service', async () => {
    requireBinary('service', binary);
    const urls = {};
    for (const [id, node] of [['1343', 'cf'], ['1344', 'nya']]) {
      await check(`api-${node}`, async () => { urls[id] = await upstream(id, node); });
    }
    const songs = path.join(lab, 'storage');
    let service;
    const cached = [];
    service = await stepstash('cold', songs);
    for (const id of ['1343', '1344']) {
      if (!urls[id]) {
        record(`video-chain-${id}`, { outcome: 'blocked', category: 'dependency', reason: 'No supported upstream video URL' });
        continue;
      }
      await check(`video-chain-${id}`, async () => {
        const url = urls[id];
        record(`cold-${id}-started`, { host: url.hostname });
        const res = await request(url.toString(), { port: service.port });
        record(`cold-${id}`, evidence(res));
        fullVideo(res, url, 'MISS');
        assert.equal(digest(fs.readFileSync(videoFile(songs, url)), 'md5'), res.md5);
        assert.equal(fs.existsSync(path.join(songs, id)), false);
        cached.push(id);
        const alternate = new URL(url);
        alternate.hostname = url.hostname === 'play.udon.dance' ? 'nya.xin.moe' : 'play.udon.dance';
        const hot = await request(alternate.toString(), { port: service.port, headers: { Range: 'bytes=0-1023' } });
        record(`cross-host-${id}`, evidence(hot));
        rangeVideo(hot, videoFile(songs, url));
      });
      await check(`playback-api-${id}`, async () => {
        const res = await request(playbackURL(id, id === '1343' ? 'cf' : 'nya'), { port: service.port });
        record(`playback-api-${id}-response`, evidence(res));
        fullVideo(res, urls[id], cached.includes(id) ? 'HIT' : 'MISS');
        assert.equal(res.headers['x-stepstash-fallback'], undefined);
      });
    }
    await stop(service.child);
    service = await stepstash('restart', songs);
    for (const id of cached) await check(`restart-${id}`, async () => {
      const res = await request(urls[id].toString(), { port: service.port, method: 'HEAD' });
      record(`restart-${id}-response`, evidence(res));
      assert.equal(res.status, 200); assert.equal(res.headers['x-stepstash-cache'], 'HIT');
      assert.equal(Number(res.headers['content-length']), Number(urls[id].searchParams.get('s')));
    });
    await stop(service.child);
  });
  await check('console-auto-and-local-fallback', async () => {
    requireBinary('console', consoleBinary);
    await consolePorts();
    const autoStorage = path.join(lab, 'console-storage');
    const auto = await consoleService('console-auto', autoStorage);
    let expected;
    try {
      // No node is the game's Auto API request. Use a fresh cache to exercise routing.
      expected = await upstream('1343', 'nya');
      const res = await request(playbackURL('1343'), { port: auto.port });
      record('console-auto-cold', evidence(res));
      fullVideo(res, expected, 'MISS');
      assert.equal(res.headers['x-stepstash-fallback'], undefined);
      const hot = await request(playbackURL('1343'), { port: auto.port, headers: { Range: 'bytes=0-1023' } });
      record('console-auto-hot', evidence(hot));
      rangeVideo(hot, videoFile(autoStorage, expected));
    } finally { await stop(auto.child); }

    // An owned TCP endpoint rejects every SOCKS handshake. No TLS bypass or OS network changes.
    let rejectedConnections = 0;
    const rejector = net.createServer(socket => { rejectedConnections++; socket.destroy(); });
    servers.add(rejector);
    await new Promise((resolve, reject) => {
      rejector.once('error', reject); rejector.listen(0, '127.0.0.1', resolve);
    });
    const offline = await consoleService('console-offline', autoStorage, {
      upstreamMode: 'socks5', socks5Address: `127.0.0.1:${rejector.address().port}`,
    });
    try {
      for (const method of ['GET', 'HEAD']) {
        const before = rejectedConnections;
        const res = await request(playbackURL('1343'), { port: offline.port, method });
        record(`local-fallback-${method}`, evidence(res));
        if (method === 'GET') fullVideo(res, expected, 'HIT');
        else {
          assert.equal(res.status, 200); assert.equal(res.bytes, 0);
          assert.equal(res.headers['x-stepstash-cache'], 'HIT');
          assert.equal(Number(res.headers['content-length']), Number(expected.searchParams.get('s')));
        }
        assert.equal(res.headers['x-stepstash-fallback'], 'upstream-unavailable');
        assert.ok(rejectedConnections > before, 'upstream failure must actually be injected');
      }
      const range = await request(playbackURL('1343'), { port: offline.port, headers: { Range: 'bytes=0-1023' } });
      record('local-fallback-range', evidence(range));
      rangeVideo(range, videoFile(autoStorage, expected));
      assert.equal(range.headers['x-stepstash-fallback'], 'upstream-unavailable');
      const missing = await request(playbackURL('1344'), { port: offline.port });
      record('local-fallback-missing', evidence(missing));
      assert.equal(missing.status, 502);
      assert.equal(missing.headers['x-stepstash-fallback'], undefined);
      assert.equal(fs.readdirSync(path.join(autoStorage, 'videos')).filter(n => n.endsWith('.mp4')).length, 1);
      record('offline-injection', { rejectedConnections });
    } finally { await stop(offline.child); }
  });
} catch (error) {
  record('setup-or-execution', { outcome: error.category ? 'blocked' : 'failed',
    category: error.category || 'program-failure', error: error.stack });
} finally {
  for (const child of children) await stop(child);
  for (const server of servers) await new Promise(resolve => server.close(resolve));
  const failed = report.checks.some(c => c.outcome === 'failed');
  const blocked = report.checks.some(c => c.outcome === 'blocked');
  report.outcome = failed ? 'failed' : blocked ? 'blocked' : 'passed';
  report.passed = report.outcome === 'passed';
  process.exitCode = failed ? 1 : blocked ? 2 : 0;
  report.finished = new Date().toISOString();
  fs.writeFileSync(path.join(lab, 'results.json'), JSON.stringify(report, null, 2));
  console.log(`Report: ${lab}`);
}
