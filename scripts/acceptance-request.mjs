import http from 'node:http';
import https from 'node:https';
import crypto from 'node:crypto';
import { upstreamLookup } from './acceptance-dns.mjs';

export function request(target, { port, method = 'GET', headers = {}, timeout = 360000, capture = false } = {}) {
  return new Promise((resolve, reject) => {
    const url = new URL(target);
    const started = Date.now();
    let firstByteMs;
    // A port override explicitly models the game's local HTTP interception.
    // Upstream DNS bypasses hosts; retain Host, SNI and normal TLS validation.
    const transport = !port && url.protocol === 'https:' ? https : http;
    const chunks = [];
    const req = transport.request({ hostname: port ? '127.0.0.1' : url.hostname, port: port || url.port || (transport === https ? 443 : 80),
      path: url.pathname + url.search, method, headers: { Host: url.host, ...headers }, agent: false, ...(!port ? { lookup: upstreamLookup } : {}) }, res => {
      firstByteMs = Date.now() - started;
      const sha = crypto.createHash('sha256'), md5 = crypto.createHash('md5');
      let bytes = 0;
      res.on('data', b => {
        bytes += b.length; sha.update(b); md5.update(b);
        if (capture && bytes <= 1024 * 1024) chunks.push(b);
        if (capture && bytes > 1024 * 1024) req.destroy(new Error('captured response exceeds 1 MiB'));
      });
      res.on('error', error => { clearTimeout(deadline); reject(error); });
      res.on('end', () => { clearTimeout(deadline); resolve({ status: res.statusCode, headers: res.headers, remoteAddress: res.socket.remoteAddress,
        ...(capture ? { body: Buffer.concat(chunks).toString('utf8') } : {}),
        bytes, sha256: sha.digest('hex'), md5: md5.digest('hex'), firstByteMs, elapsedMs: Date.now() - started }); });
    });
    const deadline = setTimeout(() => req.destroy(new Error('request deadline exceeded')), timeout);
    req.on('error', e => { clearTimeout(deadline); reject(e); });
    req.end();
  });
}
