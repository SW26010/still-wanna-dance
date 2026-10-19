import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

test('video totals exclude error responses while retaining their diagnostics', t => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'body-summary-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const events = [{ msg: 'body_probe_ready' }];
  for (const [request_id, method, status, offset, bytes] of [
    [1, 'GET', 200, 0, 8],
    [2, 'GET', 206, 4, 6],
    [3, 'GET', 416, 0, 16],
    [4, 'HEAD', 200, 0, 0],
  ]) {
    events.push(
      { msg: 'body_request', request_id, song: '1', method, player: true },
      { msg: 'body_headers', request_id, status },
      { msg: 'body_write', request_id, offset, bytes, write_ok: true, flush_ok: true },
      { msg: 'body_finished', request_id, bytes, canceled: false },
    );
  }
  const log = path.join(dir, 'probe.jsonl');
  fs.writeFileSync(log, events.map(e => JSON.stringify({ time: '2000-01-01T00:00:00Z', ...e })).join('\n'));
  const script = fileURLToPath(new URL('./body-summary.mjs', import.meta.url));
  const result = JSON.parse(execFileSync(process.execPath, [script, log], { encoding: 'utf8' }));
  assert.equal(result.requests.length, 4);
  const failed = result.requests.find(r => r.status === 416);
  assert.equal(failed.writeBytes, 16, 'error response bytes remain diagnostic data');
  assert.deepEqual(failed.intervals, [], 'error text has no video offsets');
  assert.deepEqual(result.songs, [{ song: '1', playerTotalBytes: 14, playerUniqueBytes: 10, intervals: [[0, 10]] }]);
});
