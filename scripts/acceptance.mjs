// Opt-in desktop-consent acceptance. All output stays under test-runs.
// node scripts/acceptance.mjs [console-executable] <existing-terms-receipt>
import fs from 'node:fs';
import path from 'node:path';
import net from 'node:net';
import http from 'node:http';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';
import { AcceptanceBlocked, requireAcceptedTerms } from './acceptance-protocol.mjs';

const args = process.argv.slice(2);
const [consoleExecutable, receiptPath] = args.length === 1 ? [undefined, args[0]] : args;
const root = process.cwd();
const consoleBinary = path.resolve(consoleExecutable || path.join(root, 'bin', 'SWD.exe'));
const lab = path.join(root, 'test-runs', `acceptance-${Date.now()}`);
fs.mkdirSync(lab, { recursive: true });
const report = { started: new Date().toISOString(), checks: [] };
const children = new Set();
const digest = data => crypto.createHash('sha256').update(data).digest('hex');
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

function request(port, pathname, { method = 'GET', timeout = 10000 } = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request({ hostname: '127.0.0.1', port, path: pathname, method, agent: false }, res => {
      const chunks = [];
      let bytes = 0;
      res.on('data', chunk => {
        bytes += chunk.length;
        if (bytes > 1024 * 1024) req.destroy(new Error('captured response exceeds 1 MiB'));
        else chunks.push(chunk);
      });
      res.on('error', reject);
      res.on('end', () => resolve({ status: res.statusCode, body: Buffer.concat(chunks) }));
    });
    const deadline = setTimeout(() => req.destroy(new Error('request deadline exceeded')), timeout);
    req.on('close', () => clearTimeout(deadline));
    req.on('error', reject);
    req.end();
  });
}

async function launch(executable, args, name, port) {
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
    try { await request(port, '/', { method: 'HEAD', timeout: 500 }); return child; } catch {}
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
function requireBinary(file) {
  const relative = path.relative(root, file);
  // External binaries retain only their filename; never publish machine paths.
  const reportPath = (path.isAbsolute(relative) || relative === '..' || relative.startsWith(`..${path.sep}`)
    ? path.basename(file) : relative).split(path.sep).join('/');
  if (!fs.existsSync(file)) throw new AcceptanceBlocked('environment', `Missing console binary: ${reportPath}`);
  record('binary', { component: 'console', path: reportPath, sha256: digest(fs.readFileSync(file)) });
}

try {
  if (!receiptPath || !fs.existsSync(receiptPath)) {
    throw new AcceptanceBlocked('environment', 'Provide an existing console terms.json receipt after reading and accepting the current terms.');
  }
  // Only the application may decrypt/validate consent. Copy opaque bytes;
  // never reconstruct acceptance or submit the consent API from this script.
  const receipt = fs.readFileSync(receiptPath);
  requireBinary(consoleBinary);
  const consentConfig = path.join(lab, 'consent-check.json');
  fs.writeFileSync(consentConfig, JSON.stringify({ schemaVersion: 8, autoAccelerate: false, queueWithGateway: false }));
  fs.mkdirSync(path.join(consentConfig + '.d', 'data', 'state'), { recursive: true });
  fs.writeFileSync(path.join(consentConfig + '.d', 'data', 'state', 'terms.json'), receipt);
  const consentPort = await freePort();
  const consentChild = await launch(consoleBinary, ['-no-tray', '-no-open', '-listen', `127.0.0.1:${consentPort}`,
    '-config', consentConfig], 'consent-check', consentPort);
  try {
    const status = await request(consentPort, '/api/status');
    requireAcceptedTerms(status.status);
    const terms = await request(consentPort, '/terms.txt');
    if (terms.status !== 200) throw new Error(`Terms response returned unexpected status ${terms.status}`);
    if (digest(terms.body) !== digest(fs.readFileSync(path.join(root, 'internal/legal/TERMS.txt')))) {
      throw new AcceptanceBlocked('environment', 'Console terms differ from source; rebuild before acceptance.');
    }
    record('consent', { outcome: 'passed' });
  } finally {
    await stop(consentChild);
  }
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
