// Opt-in CLI real-network and desktop-consent acceptance. All output stays under test-runs.
// Console gateway routing/fallback is covered by the Go integration suite; no hidden start API.
// node scripts/acceptance.mjs [service-executable] [console-executable] <existing-terms-receipt>
import fs from 'node:fs';
import path from 'node:path';
import net from 'node:net';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';
import assert from 'node:assert/strict';
import { request, waitForResourceRegistration } from './acceptance-request.mjs';
import { AcceptanceBlocked, videoRedirect, requireAcceptedTerms } from './acceptance-protocol.mjs';

const [executable, consoleExecutable, receiptPath] = process.argv.slice(2);
const root = process.cwd();
const binary = path.resolve(executable || path.join(root, 'bin', 'still-wanna-dance.exe'));
const consoleBinary = path.resolve(consoleExecutable || path.join(root, 'bin', 'SWD.exe'));
const lab = path.join(root, 'test-runs', `acceptance-${Date.now()}`);
fs.mkdirSync(lab, { recursive: true });
const report = { started: new Date().toISOString(), checks: [] };
const children = new Set();
const digest = (data, algorithm = 'sha256') => crypto.createHash(algorithm).update(data).digest('hex');
let receipt;
let termsVersion;
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
    ['location', 'content-length', 'content-range', 'content-type', 'etag', 'last-modified', 'x-swd-cache', 'x-swd-fallback'].includes(k)));
  return { ...result, headers };
}
async function launch(executable, args, name, port, readyURL = 'http://api.udon.dance/') {
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
async function startService(name, storageDir) {
  const port = await freePort();
  const database = path.join(storageDir, 'storage.sqlite');
  const videos = path.join(storageDir, 'videos');
  if (!fs.existsSync(database)) {
    await new Promise((resolve,reject) => {
      const child = spawn(binary, ['-database', database, '-create-database'], {stdio:'inherit'});
      child.once('error', reject);
      child.once('exit', code => code === 0 ? resolve() : reject(new Error('database creation failed: '+code)));
    });
    fs.mkdirSync(videos, {recursive:true});
  }
  const child = await launch(binary, ['-listen', `127.0.0.1:${port}`,
    '-database', database, '-video-dir', videos, '-cache-unlimited=true', '-download-timeout', '5m', '-accept-terms', termsVersion], name, port);
  return { child, port };
}
function videoFile(root, url) {
 const key = url.searchParams.get('e').toLowerCase();
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
  assert.equal(res.headers['x-swd-cache'], cache);
  assert.equal(res.headers.location, undefined, 'local playback must return video, not redirect');
  assert.equal(res.md5, url.searchParams.get('e').toLowerCase());
  assert.equal(res.bytes, Number(url.searchParams.get('s')));
}
function rangeVideo(res, file) {
  const data = fs.readFileSync(file);
  assert.equal(res.status, 206);
  assert.equal(res.headers['x-swd-cache'], 'HIT');
  assert.equal(res.headers['content-range'], `bytes 0-1023/${data.length}`);
  assert.equal(res.bytes, 1024);
  assert.equal(res.sha256, digest(data.subarray(0, 1024)));
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
  if (!receiptPath || !fs.existsSync(receiptPath)) {
    throw new AcceptanceBlocked('environment', 'Provide an existing console terms.json receipt as the third argument after reading and accepting the current terms.');
  }
  // Only the application may decrypt/validate consent. Copy opaque bytes;
  // never reconstruct acceptance or submit the consent API from this script.
  receipt = fs.readFileSync(receiptPath);
  requireBinary('console', consoleBinary);
  const consentConfig = path.join(lab, 'consent-check.json');
  fs.writeFileSync(consentConfig, JSON.stringify({ schemaVersion: 7, autoAccelerate: false, queueWithGateway: false }));
  fs.mkdirSync(path.join(consentConfig + '.d', 'data', 'state'), {recursive:true});
  fs.writeFileSync(path.join(consentConfig + '.d', 'data', 'state', 'terms.json'), receipt);
  const consentPort = await freePort();
  const consentURL = `http://127.0.0.1:${consentPort}`;
  const consentChild = await launch(consoleBinary, ['-no-tray', '-no-open', '-listen', `127.0.0.1:${consentPort}`,
    '-config', consentConfig], 'consent-check', consentPort, consentURL);
  try {
    const status = await request(`${consentURL}/api/status`, { capture: true, timeout: 10000 });
    requireAcceptedTerms(status.status);
    const terms = await request(`${consentURL}/terms.txt`, { capture: true, timeout: 10000 });
    assert.equal(terms.status, 200);
    if (digest(terms.body) !== digest(fs.readFileSync(path.join(root, 'internal/legal/TERMS.txt')))) {
      throw new AcceptanceBlocked('environment', 'Console terms differ from source; rebuild before acceptance.');
    }
    termsVersion = fs.readFileSync(path.join(root, 'internal/legal/legal.go'), 'utf8').match(/const Version = "([^"]+)"/)?.[1];
    assert.ok(termsVersion);
  } finally {
    await stop(consentChild);
  }
  await check('service', async () => {
    requireBinary('service', binary);
    const urls = {};
    for (const [id, node] of [['1343', 'cf'], ['1344', 'nya']]) {
      await check(`api-${node}`, async () => { urls[id] = await upstream(id, node); });
    }
    const songs = path.join(lab, 'storage');
    let service;
    const cached = [];
    service = await startService('cold', songs);
    for (const id of ['1343', '1344']) {
      if (!urls[id]) {
        record(`video-chain-${id}`, { outcome: 'blocked', category: 'dependency', reason: 'No supported upstream video URL' });
        continue;
      }
      await check(`video-chain-${id}`, async () => {
        const url = urls[id];
        record(`cold-${id}-started`, { host: url.hostname });
        const res = await request(playbackURL(id, id === '1343' ? 'cf' : 'nya'), { port: service.port });
        record(`cold-${id}`, evidence(res));
        fullVideo(res, url, 'MISS');
        assert.equal(digest(fs.readFileSync(videoFile(songs, url)), 'md5'), res.md5);
        assert.equal(fs.existsSync(path.join(songs, id)), false);
        cached.push(id);
        const alternateNode = id === '1343' ? 'nya' : 'cf';
        const alternate = await upstream(id, alternateNode);
        if (alternate.hostname !== url.hostname && alternate.searchParams.get('e') === url.searchParams.get('e') && alternate.searchParams.get('s') === url.searchParams.get('s')) {
          await request(playbackURL(id, alternateNode), { port: service.port, method: 'HEAD' });
          await waitForResourceRegistration(alternate.toString(), { port: service.port });
          const hot = await request(alternate.toString(), { port: service.port, headers: { Range: 'bytes=0-1023' } });
          record(`cross-host-${id}`, evidence(hot));
          rangeVideo(hot, videoFile(songs, url));
        } else {
          record(`cross-host-${id}`, { outcome: 'skipped', reason: 'API did not return a distinct domain for the same content' });
        }
      });
      await check(`playback-api-${id}`, async () => {
        const res = await request(playbackURL(id, id === '1343' ? 'cf' : 'nya'), { port: service.port });
        record(`playback-api-${id}-response`, evidence(res));
        fullVideo(res, urls[id], cached.includes(id) ? 'HIT' : 'MISS');
        assert.equal(res.headers['x-swd-fallback'], undefined);
      });
    }
    await stop(service.child);
    service = await startService('restart', songs);
    for (const id of cached) await check(`restart-${id}`, async () => {
      const res = await request(playbackURL(id, id === '1343' ? 'cf' : 'nya'), { port: service.port, method: 'HEAD' });
      record(`restart-${id}-response`, evidence(res));
      assert.equal(res.status, 200); assert.equal(res.headers['x-swd-cache'], 'HIT');
      assert.equal(Number(res.headers['content-length']), Number(urls[id].searchParams.get('s')));
    });
    await stop(service.child);
  });
} catch (error) {
  record('setup-or-execution', { outcome: error.category ? 'blocked' : 'failed',
    category: error.category || 'program-failure', error: error.stack });
} finally {
  for (const child of children) await stop(child);
  const failed = report.checks.some(c => c.outcome === 'failed');
  const blocked = report.checks.some(c => c.outcome === 'blocked');
  report.outcome = failed ? 'failed' : blocked ? 'blocked' : 'passed';
  report.passed = report.outcome === 'passed';
  process.exitCode = failed ? 1 : blocked ? 2 : 0;
  report.finished = new Date().toISOString();
  fs.writeFileSync(path.join(lab, 'results.json'), JSON.stringify(report, null, 2));
  console.log(`Report: ${lab}`);
}
