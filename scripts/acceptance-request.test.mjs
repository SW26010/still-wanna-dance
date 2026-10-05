import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import https from 'node:https';
import { createUpstreamLookup, upstreamLookup } from './acceptance-dns.mjs';
import { request, waitForResourceRegistration } from './acceptance-request.mjs';

const lookup = (fn, all = false) => new Promise((resolve, reject) =>
  fn('api.udon.dance', { all }, (error, address, family) => error ? reject(error) : resolve({ address, family })));

test('independent DNS filters local answers and supports Node lookup callback modes', async () => {
  const fn = createUpstreamLookup(async host => {
    assert.equal(host, 'api.udon.dance');
    return ['127.0.0.1', '10.1.2.3', '192.168.1.2', '172.16.0.1', '169.254.1.1',
      '0.0.0.0', '224.0.0.1', '100.64.0.1', '::1', '8.8.8.8', '1.1.1.1'];
  }, () => ({ ethernet: [{ address: '8.8.8.8' }] }));
  assert.deepEqual(await lookup(fn), { address: '1.1.1.1', family: 4 });
  assert.deepEqual(await lookup(fn, true), { address: [{ address: '1.1.1.1', family: 4 }], family: undefined });
});

test('DNS failure or only local answers fails closed without OS lookup', async () => {
  await assert.rejects(lookup(createUpstreamLookup(async () => { throw new Error('DNS unavailable'); })), /DNS unavailable/);
  await assert.rejects(lookup(createUpstreamLookup(async () => ['127.0.0.1', '::1'])), /no non-local/);
});

test('HTTP and HTTPS upstream probes install independent lookup and preserve TLS hostname', async t => {
  for (const transport of [http, https]) {
    const scheme = transport === https ? 'https' : 'http';
    const sentinel = new Error('request inspected');
    const mock = t.mock.method(transport, 'request', options => {
      assert.equal(options.lookup, upstreamLookup);
      assert.equal(options.hostname, 'api.udon.dance');
      assert.equal(options.headers.Host, 'api.udon.dance');
      assert.equal(options.rejectUnauthorized, undefined);
      assert.equal(options.agent, false);
      throw sentinel;
    });
    await assert.rejects(request(`${scheme}://api.udon.dance/Api/Songs/play?id=1343`), error => error === sentinel);
    mock.mock.restore();
  }
});

test('explicit local port still reaches owned server with original Host and path', async () => {
  const server = http.createServer((req, res) => {
    assert.equal(req.headers.host, 'api.udon.dance');
    assert.equal(req.url, '/Api/Songs/play?id=1343');
    res.end('local video');
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    const res = await request('http://api.udon.dance/Api/Songs/play?id=1343', { port: server.address().port, capture: true });
    assert.equal(res.body, 'local video');
    assert.equal(res.remoteAddress, '127.0.0.1');
  } finally { await new Promise(resolve => server.close(resolve)); }
});

test('cross-host acceptance waits for registration and bounds missing-domain waits', async t => {
 let calls = 0, status = 400;
 const server = http.createServer((req, res) => {
  assert.equal(req.method, 'HEAD');
  calls++;
  res.writeHead(calls >= 3 ? status : 400); res.end();
 });
 await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
 t.after(() => new Promise(resolve => server.close(resolve)));
 const options = { port: server.address().port, interval: 1, timeout: 2000 };
 status = 200;
 assert.equal((await waitForResourceRegistration('http://media.example/files/test', options)).status, 200);
 assert.equal(calls, 3);
 status = 503;
 await assert.rejects(waitForResourceRegistration('http://media.example/files/test', options), /HTTP 503/);
 status = 400;
 await assert.rejects(waitForResourceRegistration('http://media.example/files/test', { ...options, timeout: 40 }), /deadline/);
});
