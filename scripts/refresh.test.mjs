import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

const html = readFileSync(new URL('../internal/console/index.html', import.meta.url), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
const flush = () => new Promise(resolve => setImmediate(resolve));

function page(hidden = false) {
  const requests = [];
  const timers = new Map();
  const elements = new Map();
  const listeners = new Map();
  let nextTimer = 0;
  const document = {
    hidden,
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, { addEventListener() {}, replaceChildren() {} });
      return elements.get(id);
    },
    querySelectorAll: () => [],
    addEventListener: (event, callback) => listeners.set(event, callback),
  };
  const context = vm.createContext({
    document,
    fetch(url) {
      return new Promise((resolve, reject) => requests.push({
        url, reject,
        finish(data = {}) { resolve({ ok: true, json: async () => data }); },
      }));
    },
    setTimeout(callback, delay) {
      timers.set(++nextTimer, { callback, delay });
      return nextTimer;
    },
    clearTimeout: id => timers.delete(id),
  });
  vm.runInContext(script, context);
  return {
    requests, timers, context,
    visibility(hidden) {
      document.hidden = hidden;
      listeners.get('visibilitychange')();
    },
    finishBatch(start = 0) {
      requests[start].finish({ settings: {}, hosts: {}, batch: {}, queue: {} });
      requests[start + 1].finish({ bytes: 0 });
    },
    fireTimer() {
      assert.equal(timers.size, 1);
      const [id, timer] = [...timers][0];
      timers.delete(id);
      timer.callback();
    },
  };
}

test('periodic refresh waits for both reads and keeps exactly one timer', async () => {
  const p = page();
  assert.deepEqual(p.requests.map(r => r.url), ['/api/status', '/api/inventory']);
  p.requests[0].finish({ settings: {}, hosts: {}, batch: {}, queue: {} });
  await flush();
  assert.equal(p.timers.size, 0);
  p.requests[1].finish({ bytes: 0 });
  await flush();
  assert.equal(p.timers.size, 1);
  assert.equal([...p.timers.values()][0].delay, 5000);
  p.fireTimer();
  assert.equal(p.requests.length, 4);
  assert.equal(p.timers.size, 0);
  p.finishBatch(2);
  await flush();
  assert.equal(p.timers.size, 1);
});

test('action completion during a slow refresh queues a fresh batch and merges triggers', async () => {
  const p = page();
  const action = vm.runInContext("action('start')", p.context);
  assert.equal(p.requests[2].url, '/api/start');
  p.requests[2].finish();
  await flush();
  p.visibility(false);
  p.visibility(false);
  assert.equal(p.requests.length, 3);
  p.finishBatch();
  await flush();
  assert.equal(p.requests.length, 5);
  assert.equal(p.timers.size, 0);
  p.finishBatch(3);
  await action;
  assert.equal(p.requests.length, 5);
  assert.equal(p.timers.size, 1);
});

test('hidden pages pause polling and refresh immediately on return', async () => {
  const p = page();
  p.finishBatch();
  await flush();
  p.visibility(true);
  assert.equal(p.timers.size, 0);
  assert.equal(p.requests.length, 2);
  p.visibility(false);
  assert.equal(p.requests.length, 4);
  assert.equal(p.timers.size, 0);
  p.visibility(true);
  p.finishBatch(2);
  await flush();
  assert.equal(p.timers.size, 0);
  assert.equal(p.requests.length, 4);
});

test('a page opened in the background waits until visible', async () => {
  const p = page(true);
  assert.equal(p.requests.length, 0);
  assert.equal(p.timers.size, 0);
  p.visibility(false);
  assert.equal(p.requests.length, 2);
  p.finishBatch();
  await flush();
  assert.equal(p.timers.size, 1);
  assert.match(vm.runInContext("$('inventoryState').textContent", p.context), /请点击「扫描本地文件」/);
});

test('hiding drops queued refreshes and defers action refresh until visible', async () => {
  const p = page();
  p.visibility(false); // Queue a second batch behind the initial reads.
  const action = vm.runInContext("action('inventory/scan')", p.context);
  p.visibility(true);
  p.requests[2].finish();
  await action;
  p.finishBatch();
  await flush();
  assert.equal(p.requests.length, 3);
  assert.equal(p.timers.size, 0);
  p.visibility(false);
  assert.equal(p.requests.length, 5);
  p.finishBatch(3);
  await flush();
  assert.equal(p.timers.size, 1);
});

test('failed reads leave polling able to recover', async () => {
  const p = page();
  for (const request of p.requests) request.reject(new Error('offline'));
  await flush();
  p.fireTimer();
  p.finishBatch(2);
  await flush();
  assert.equal(p.timers.size, 1);
  assert.equal(vm.runInContext("$('connection').textContent", p.context), '● 控制台已连接');
});
