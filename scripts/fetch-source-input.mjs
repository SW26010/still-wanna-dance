// Download a reviewed non-Go source archive. Never execute downloaded contents.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { sha256, run } from './release-source.mjs';
try {
  const [url, output, expected] = process.argv.slice(2);
  if (new URL(url).protocol !== 'https:' || !/^[a-f0-9]{64}$/.test(expected)) throw Error('Invalid source input identity');
  if (process.platform === 'win32') {
    run('pwsh', ['-NoProfile', '-File', path.join(path.dirname(fileURLToPath(import.meta.url)), 'download-source-input.ps1'), '-Url', url, '-Output', output]);
  } else {
    const response = await fetch(url, { signal: AbortSignal.timeout(180000) });
    if (response.status !== 200 || !response.body) throw Error(`Source input HTTP ${response.status}`);
    await pipeline(Readable.fromWeb(response.body), fs.createWriteStream(output, { flags: 'wx' }));
  }
  if (sha256(output) !== expected) throw Error('Source input checksum mismatch');
} catch (e) { console.error(e.message); process.exitCode = 1; }
