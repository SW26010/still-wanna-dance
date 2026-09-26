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
    querySelectorAll: selector => selector === 'button'
      ? [...html.matchAll(/<button\b[^>]*\bid="([^"]+)"/g)].map(match => document.getElementById(match[1]))
      : [],
    addEventListener: (event, callback) => listeners.set(event, callback),
  };
  const context = vm.createContext({
    document,
    AbortController,
    fetch(url, { signal } = {}) {
      return new Promise((resolve, reject) => {
        let rejectBody;
        signal?.addEventListener('abort', () => {
          reject(signal.reason);
          rejectBody?.(signal.reason);
        }, { once: true });
        requests.push({
          url, signal, reject,
          finish(data = {}, ok = true) { resolve({ ok, json: async () => data }); },
          headersOnly() {
            resolve({ ok: true, json: () => new Promise((_, reject) => { rejectBody = reject; }) });
          },
        });
      });
    },
    setTimeout(callback, delay) {
      timers.set(++nextTimer, { callback, delay });
      return nextTimer;
    },
    clearTimeout: id => timers.delete(id),
  });
  vm.runInContext(script, context);
  function fireTimer(delay) {
    const matches = [...timers].filter(([, timer]) => timer.delay === delay);
    assert.equal(matches.length, 1);
    const [id, timer] = matches[0];
    timers.delete(id);
    timer.callback();
  }
  return {
    requests, context,
    get timers() { return new Map([...timers].filter(([, timer]) => timer.delay === 5000)); },
    get deadlines() { return new Map([...timers].filter(([, timer]) => timer.delay === 10000)); },
    visibility(hidden) {
      document.hidden = hidden;
      listeners.get('visibilitychange')();
    },
    finishBatch(start = 0) {
      requests[start].finish({ settings: {}, hosts: {}, batch: {}, queue: {} });
      requests[start + 1].finish({ bytes: 0 });
    },
    fireTimer: () => fireTimer(5000),
    expireRead: () => fireTimer(10000),
    expireAction: hosts => fireTimer(hosts ? 120000 : 30000),
    get actionDeadlines() { return [...timers.values()].filter(t => [30000, 120000].includes(t.delay)); },
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

for (const stalled of [0, 1]) {
  for (const bodyStalls of [false, true]) {
    test(`${stalled === 0 ? 'status' : 'inventory'} timeout cancels a stalled ${bodyStalls ? 'body' : 'fetch'} and polling recovers`, async () => {
      const p = page();
      assert.equal(p.deadlines.size, 2);
      if (bodyStalls) p.requests[stalled].headersOnly();
      p.requests[1 - stalled].finish({ settings: {}, hosts: {}, batch: {}, queue: {}, bytes: 0 });
      await flush();
      assert.equal(p.deadlines.size, 1);
      assert.equal(p.timers.size, 0);
      p.expireRead();
      await flush();
      assert.equal(p.requests[stalled].signal.aborted, true);
      assert.equal(p.deadlines.size, 0);
      assert.equal(p.timers.size, 1);
      assert.match(vm.runInContext(stalled === 0 ? "$('connection').textContent" : "$('inventoryState').textContent", p.context), /中断|暂不可用/);
      p.fireTimer();
      p.finishBatch(2);
      await flush();
      assert.equal(p.deadlines.size, 0);
      assert.equal(p.timers.size, 1);
      assert.equal(vm.runInContext("$('connection').textContent", p.context), '● 控制台已连接');
      assert.match(vm.runInContext("$('inventoryState').textContent", p.context), /请点击/);
    });
  }

  for (const trigger of ['visibility', 'action']) {
    test(`${trigger} refresh resumes after read ${stalled} times out`, async () => {
      const p = page();
      p.requests[1 - stalled].finish({ settings: {}, hosts: {}, batch: {}, queue: {}, bytes: 0 });
      let action;
      if (trigger === 'action') {
        action = vm.runInContext("action('start')", p.context);
        assert.equal(p.requests[2].signal.aborted, false);
        p.requests[2].finish();
      } else {
        p.visibility(true);
        p.visibility(false);
      }
      await flush();
      const next = p.requests.length;
      p.expireRead();
      await flush();
      assert.equal(p.requests[stalled].signal.aborted, true);
      assert.equal(p.requests.length, next + 2);
      assert.equal(p.timers.size, 0);
      p.finishBatch(next);
      await action;
      await flush();
      assert.equal(p.deadlines.size, 0);
      assert.equal(p.timers.size, 1);
    });
  }
}

for (const path of ['start', 'hosts/enable']) {
  for (const bodyStalls of [false, true]) {
    test(`${path} timeout covers stalled ${bodyStalls ? 'body' : 'fetch'}, unlocks controls and checks state without retrying`, async () => {
      const p = page();
      p.finishBatch();
      await flush();
      const action = vm.runInContext(`action('${path}')`, p.context);
      assert.equal(vm.runInContext("$('start').disabled && $('inventoryScan').disabled", p.context), true);
      assert.equal(p.actionDeadlines[0].delay, path.startsWith('hosts/') ? 120000 : 30000);
      await vm.runInContext(`action('${path}')`, p.context);
      assert.equal(p.requests.length, 3);
      if (bodyStalls) p.requests[2].headersOnly();
      await flush();
      p.expireAction(path.startsWith('hosts/'));
      await flush();
      assert.equal(p.requests[2].signal.aborted, true);
      assert.equal(vm.runInContext('busy', p.context), false);
      assert.equal(p.actionDeadlines.length, 0);
      assert.deepEqual(p.requests.slice(3).map(r => r.url), ['/api/status', '/api/inventory']);
      p.finishBatch(3);
      await action;
      assert.equal(vm.runInContext("$('start').disabled || $('inventoryScan').disabled", p.context), false);
      assert.match(vm.runInContext("$('notice').textContent", p.context), /结果尚未确认.*请勿重复提交/s);
      assert.match(vm.runInContext("$('notice').textContent", p.context), /已刷新当前状态/);
      assert.equal(p.requests.filter(r => r.url === '/api/' + path).length, 1);
    });
  }
}

test('lost action response keeps the uncertainty warning through failed state checks and recovery', async () => {
  const p = page();
  p.finishBatch();
  await flush();
  const action = vm.runInContext("action('batch/scan')", p.context);
  p.requests[2].reject(new Error('network lost'));
  await flush();
  p.requests[3].reject(new Error('offline'));
  p.requests[4].finish({ bytes: 0 });
  await action;
  assert.equal(p.actionDeadlines.length, 0);
  assert.match(vm.runInContext("$('notice').textContent", p.context), /结果尚未确认.*无法连接/s);
  assert.equal(vm.runInContext("$('start').disabled", p.context), true);
  p.fireTimer();
  p.finishBatch(5);
  await flush();
  assert.equal(vm.runInContext("$('start').disabled", p.context), false);
  assert.match(vm.runInContext("$('notice').textContent", p.context), /结果尚未确认.*已刷新当前状态/s);
});

for (const ok of [true, false]) {
  test(`complete ${ok ? 'successful' : 'failed'} action clears its deadline and retains its result`, async () => {
    const p = page();
    p.finishBatch();
    await flush();
    const action = vm.runInContext("action('start')", p.context);
    p.requests[2].finish(ok ? { ok: true } : { error: '端口已占用' }, ok);
    await flush();
    assert.equal(p.actionDeadlines.length, 0);
    p.finishBatch(3);
    await action;
    assert.equal(vm.runInContext('uncertainAction', p.context), '');
    assert.match(vm.runInContext("$('notice').textContent", p.context), ok ? /CDN 已启动/ : /端口已占用/);
  });
}

test('action timeout while hidden releases busy and checks state on return', async () => {
  const p = page();
  p.finishBatch();
  await flush();
  const action = vm.runInContext("action('hosts/disable')", p.context);
  p.visibility(true);
  p.expireAction(true);
  await action;
  assert.equal(vm.runInContext('busy', p.context), false);
  assert.equal(p.requests.length, 3);
  p.visibility(false);
  p.finishBatch(3);
  await flush();
  assert.match(vm.runInContext("$('notice').textContent", p.context), /结果尚未确认.*hosts 状态.*已刷新当前状态/s);
});
