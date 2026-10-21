// Isolated navigation research. No hosts or browser settings are changed.
import http from 'node:http';
import net from 'node:net';
import fs from 'node:fs';
import path from 'node:path';

const suffix = process.argv[2];
if (!suffix || !/^[a-z0-9-]+\.127\.0\.0\.2\.sslip\.io$/.test(suffix)) {
  throw new Error('Supply a fresh suffix: swd-<run>.127.0.0.2.sslip.io');
}
const output = path.resolve('artifacts/browser-https-research', `${suffix}.jsonl`);
fs.mkdirSync(path.dirname(output), { recursive: true });
const record = event => {
  const line = JSON.stringify({ time: new Date().toISOString(), ...event });
  fs.appendFileSync(output, line + '\n');
  console.log(line);
};
const page = `<!doctype html><meta charset="utf-8"><title>HTTPS navigation research</title>
<h1>HTTPS navigation research</h1><p>Isolated loopback services. No browser settings or hosts changes.</p>
<ul><li><a href="http://direct.${suffix}/result">HTTP link</a></li>
<li><a href="/redirect">302 redirect to HTTP</a></li>
<li><a href="https://explicit.${suffix}/result">Explicit HTTPS link</a></li></ul>`;
const server = http.createServer((req, res) => {
  record({ event: 'http', host: req.headers.host, method: req.method, path: req.url, ua: req.headers['user-agent'] });
  if (req.url === '/redirect') {
    res.writeHead(302, { Location: `http://redirect.${suffix}/result`, 'Cache-Control': 'no-store' });
    res.end();
    return;
  }
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' });
  res.end(req.url === '/result'
    ? '<!doctype html><title>Local HTTP reached</title><h1>Local HTTP reached</h1><p>HTTP content served by the isolated loopback test server.</p>'
    : page);
});
const secure = net.createServer(socket => {
  record({ event: 'tcp443' });
  socket.setTimeout(5000, () => socket.destroy());
  socket.once('data', data => {
    // Diagnostic substring only, not a general TLS/SNI parser. Longest first.
    const ascii = data.toString('latin1');
    const label = ['redirect', 'explicit', 'direct'].find(x => ascii.includes(`${x}.${suffix}`)) ?? 'unknown';
    record({ event: 'tls-client-hello-closed', label, recordType: data[0], bytes: data.length });
    socket.destroy();
  });
  socket.on('error', error => record({ event: 'socket-error', code: error.code }));
});

let stopping = false;
function stop() {
  if (stopping) return;
  stopping = true;
  record({ event: 'stop' });
  server.closeAllConnections();
  server.close();
  secure.close();
  setTimeout(() => process.exit(0), 500);
}
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
process.stdin.on('data', data => { if (data.toString().trim() === 'q') stop(); });
await Promise.all([server, secure].map((listener, index) => new Promise((resolve, reject) => {
  listener.once('error', reject);
  listener.listen(index === 0 ? 80 : 443, '127.0.0.2', resolve);
})));
record({ event: 'ready', entry: 'http://127.0.0.2/', suffix, output, tlsMode: 'close-after-ClientHello' });
setTimeout(stop, 10 * 60 * 1000).unref();
