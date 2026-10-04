import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

const html = readFileSync(new URL('../internal/console/index.html', import.meta.url), 'utf8');
const script = readFileSync(new URL('../internal/console/assets/console.js', import.meta.url), 'utf8');
const cacheScript = readFileSync(new URL('../internal/console/assets/cache.js', import.meta.url), 'utf8');
const flush = () => new Promise(resolve => setImmediate(resolve));

test('canonical monitor renders every channel, throughput, unknown and stale states', async () => {
  const p = page();
  p.finishBatch(0, {}, { settings: { upstreamMode: 'auto' }, upstreamMonitor: {
    checking: false, results: [
      { operation: 'resource', route: 'cf', mode: 'direct', ip: '1.2.3.4', channelID: 'direct/cf/1.2.3.4', state: 'available', entry: 'https://play.udon.dance', estimatedSpeedBPS: 2048, transferDurationMS: 2000, transferredBytes: 16777216, sampleSongID: 42 },
      { operation: 'resource', route: 'cf', mode: 'socks5', state: 'unavailable', reason: 'origin_timeout', http: 524 },
      { operation: 'resource', route: 'hkg', mode: 'direct', ip: '2.3.4.5', state: 'unknown', reason: 'no_sample' },
      { operation: 'catalog', route: 'api', mode: 'direct', state: 'stale', reason: 'expired', catalogTime: '20261004235822' },
      { operation: 'catalog', route: 'kiva', mode: 'socks5', state: 'available', catalogTime: '20261005010000', entry: 'https://x.kiva.moe/api/v2/wanna/songs', estimatedLatencyMS: 24 },
      { operation: 'catalog', route: 'wanna', mode: 'direct', state: 'available', catalogTime: '20260215004959', entry: 'https://wanna.kiva.moe/api/wannaInfo' },
      { operation: 'catalog', route: 'wanna', mode: 'socks5', state: 'available', catalogTime: '20260215005000', entry: 'https://wanna.kiva.moe/api/wannaInfo' },
      { operation: 'playback_url', route: 'hkg', mode: 'direct', state: 'available', estimatedLatencyMS: 12 },
      { operation: 'playback_url', route: 'cf', mode: 'socks5', state: 'unavailable', reason: 'timeout' },
    ],
  } });
  await flush();
  const get = id => p.document.getElementById(id);
  const rows = [...get('healthResourceCf').children, ...get('healthResourceHkg').children, ...get('healthCatalog').children];
  assert.equal(rows.length, 4);
  assert.match(rows[0].children[1].textContent, /2.0 KiB\/s.*歌曲 #42/);
  assert.match(rows[0].children[1].textContent, /下载 2.00 s \/ 16.00 MiB/);
  assert.match(rows[1].children[0].textContent, /源站响应超时/);
  assert.match(rows[2].children[0].textContent, /待测/);
  assert.match(rows[3].children[0].textContent, /已过期/);
  assert.equal(get('healthCatalogTime').textContent, '响应 time：20261004235822');
  assert.equal(get('healthCatalogKivaTime').textContent, '响应 time：20261005010000');
  assert.equal(get('healthCatalogWannaTime').textContent, '响应 time：20260215004959 / 20260215005000');
  assert.equal(get('healthCatalogWanna').children.length, 2);
  assert.match(get('healthCatalogWanna').children[0].children[1].textContent, /响应 time：20260215004959/);
  assert.equal(get('healthCatalogKiva').children.length, 1);
  assert.match(get('healthCatalogKiva').children[0].children[1].textContent, /x\.kiva\.moe.*首字节 24.0 ms/);
  assert.equal(get('healthPlaybackHkg').children.length, 1);
  assert.match(get('healthPlaybackHkg').children[0].children[1].textContent, /首字节 12.0 ms/);
  assert.equal(get('healthPlaybackCf').children.length, 1);
  assert.match(get('healthPlaybackCf').children[0].children[0].textContent, /SOCKS5.*请求超时/);
  assert.match(get('healthRoute').textContent, /全部直连 IP 与 SOCKS5/);
  assert.equal(get('healthCheck').disabled, false);
  p.fireTimer();
  p.finishBatch(2, {}, { settings: { upstreamMode: 'direct' }, upstreamMonitor: { checking: true, results: [] } });
  await flush();
  assert.match(get('healthSummary').textContent, /正在检测/);
  for (const id of ['healthCatalog', 'healthCatalogKiva', 'healthCatalogWanna', 'healthPlaybackHkg', 'healthPlaybackCf', 'healthResourceHkg', 'healthResourceCf']) {
    assert.equal(get(id).children.length, 1);
    assert.match(get(id).children[0].textContent, /正在检测/);
  }
  assert.equal(get('healthCheck').disabled, true);
  for (const id of ['healthCatalogTime', 'healthCatalogKivaTime', 'healthCatalogWannaTime']) assert.equal(get(id).textContent, '响应 time：未获取');
});

test('automatic resource probes display business pause and resume', async () => {
  const p = page(), get = id => p.document.getElementById(id);
  p.finishBatch(0, {}, { upstreamMonitor: { checking: true, resourcesPaused: true, results: [] } });
  await flush();
  assert.match(get('healthSummary').textContent, /业务正在加载视频，自动资源测速已暂停/);
  assert.equal(get('healthCheck').disabled, true);
  p.fireTimer();
  p.finishBatch(2, {}, { upstreamMonitor: { checking: true, resourcesPaused: false, results: [] } });
  await flush();
  assert.match(get('healthSummary').textContent, /正在检测/);
  assert.doesNotMatch(get('healthSummary').textContent, /已暂停/);
});

test('budget stop shows a settings link and clears for the next task', async () => {
  const p = page();
  p.finishBatch(0, {}, { batch: { budgetReached: true, phase: '容量预算不足' } });
  await flush();
  assert.equal(p.document.getElementById('batchBudgetHint').hidden, false);
  assert.match(html, /id="batchBudgetHint"[^>]*>[\s\S]*?href="#\/settings"/);
  p.fireTimer();
  p.finishBatch(2, {}, { batch: { running: true, budgetReached: false } });
  await flush();
  assert.equal(p.document.getElementById('batchBudgetHint').hidden, true);
});

test('exit confirms, submits once, and stops polling after acknowledgement', async () => {
  const p = page();
  p.finishBatch(); await flush();
  const get = id => p.document.getElementById(id);
  p.context.window = { confirm: () => false };
  const before = p.requests.length;
  await vm.runInContext("action('exit')", p.context);
  assert.equal(p.requests.length, before);
  p.context.window.confirm = () => true;
  const pending = vm.runInContext("action('exit')", p.context);
  assert.equal(p.requests.at(-1).url, '/api/exit');
  await vm.runInContext("action('exit')", p.context);
  assert.equal(p.requests.length, before + 1);
  p.requests.at(-1).finish({ ok: true });
  await pending;
  assert.equal(get('exit').disabled, true);
  assert.equal(get('start').disabled, true);
  assert.match(get('notice').textContent, /hosts 映射仍保留/);
  assert.equal(p.timers.size, 0);
  p.visibility(true); p.visibility(false);
  selectPage(p, 'monitor');
  await flush();
  assert.equal(p.requests.length, before + 1);
});

test('rejected exit keeps the console usable and polling', async () => {
  const p = page();
  p.finishBatch(); await flush();
  p.context.window = { confirm: () => true };
  const pending = vm.runInContext("action('exit')", p.context);
  p.requests.at(-1).finish({ error: '退出请求被拒绝' }, false);
  await flush();
  p.finishBatch(3);
  await pending;
  assert.match(p.document.getElementById('notice').textContent, /退出请求被拒绝/);
  assert.equal(p.document.getElementById('exit').disabled, false);
  assert.equal(p.timers.size, 1);
});

test('status arriving after exit cannot overwrite acknowledgement or restart polling', async () => {
  const p = page();
  p.finishBatch(); await flush();
  p.fireTimer();
  p.context.window = { confirm: () => true };
  const pending = vm.runInContext("action('exit')", p.context);
  p.requests.at(-1).finish({ ok: true });
  await pending;
  p.finishBatch(2);
  await flush();
  assert.equal(p.document.getElementById('connection').textContent, '退出请求已接受');
  assert.equal(p.document.getElementById('exit').disabled, true);
  assert.equal(p.timers.size, 0);
});

function selectPage(p, key) {
  for (const name of ['home', 'monitor', 'cache', 'library', 'settings'])
    p.document.getElementById('page-' + name).hidden = name !== key;
  p.document.dispatchEvent({ type: 'pagechange' });
}

test('initial, periodic and navigation refreshes read only the visible page plus global status', async () => {
  for (const key of ['home', 'settings', 'library', 'cache', 'monitor']) {
    const p = page(true), calls = [];
    p.context.fetch = async url => {
      calls.push(url);
      return { ok: true, json: async () => url === '/api/status'
        ? { settings: {}, hosts: {}, batch: {}, queue: {} }
        : url === '/api/inventory' ? { bytes: 0 }
        : url === '/api/downloads' ? { tasks: [], bytesPerSecond: 0 }
        : { storageID: 'test', requests: [], hasMore: false } };
    };
    selectPage(p, key);
    assert.deepEqual(calls, []);
    const expected = ['/api/status', ...(key === 'cache' ? ['/api/inventory']
      : key === 'monitor' ? ['/api/requests?limit=50', '/api/downloads'] : [])];
    p.visibility(false);
    await flush();
    assert.deepEqual(calls.splice(0), expected);
    p.fireTimer();
    await flush();
    assert.deepEqual(calls.splice(0), expected);
    selectPage(p, key === 'settings' ? 'home' : 'settings');
    await flush();
    assert.deepEqual(calls, ['/api/status']);
    assert.equal(p.timers.size, 1);
  }
});

test('page switches during an active batch coalesce and load the latest page without overlap', async () => {
  const p = page(true), calls = [];
  let finish;
  p.context.fetch = url => {
    calls.push(url);
    return new Promise(resolve => { finish = () => resolve({ ok: true,
      json: async () => ({ settings: {}, hosts: {}, batch: {}, queue: {} }) }); });
  };
  selectPage(p, 'settings');
  p.visibility(false);
  selectPage(p, 'monitor');
  selectPage(p, 'cache');
  selectPage(p, 'home');
  assert.deepEqual(calls, ['/api/status']);
  finish(); await flush();
  assert.deepEqual(calls, ['/api/status', '/api/status']);
  finish(); await flush();
  assert.equal(p.timers.size, 1);
  p.visibility(true);
  selectPage(p, 'monitor');
  assert.equal(calls.length, 2);
  assert.equal(p.timers.size, 0);
});

test('activation separates current service, hosts and session evidence, with progress and retry', () => {
  const p = page(true), get = id => p.document.getElementById(id);
  p.context.snapshot = { running: true, hosts: { ready: false, message: '未接入' }, settings: {}, batch: {}, queue: {},
    traffic: { requests: 999 }, activation: { phase: 'hosts', started: '2026-09-27T01:00:00Z' } };
  vm.runInContext('connected = true; render(snapshot)', p.context);
  assert.equal(get('enableAcceleration').disabled, true);
  assert.match(get('activationProgress').textContent, /UAC/);
  assert.doesNotMatch(get('activationRequest').textContent, /已收到/);
  p.context.snapshot.activation = { phase: 'failed', error: 'UAC canceled' };
  vm.runInContext('render(snapshot)', p.context);
  assert.equal(get('enableAcceleration').disabled, false);
  assert.match(get('enableAcceleration').textContent, /重试/);
  assert.match(get('activationNext').textContent, /服务已运行，接入尚未完成/);
  assert.match(get('activationProgress').textContent, /UAC canceled/);
  p.context.snapshot.hosts.ready = true;
  p.context.snapshot.activation = { phase: 'waiting', firstRequest: '2026-09-27T01:01:00Z' };
  vm.runInContext('render(snapshot)', p.context);
  assert.match(get('activationRequest').textContent, /已收到视频请求（不代表播放成功）/);
  assert.match(get('enableAcceleration').textContent, /重新检测/);
  p.context.snapshot.activation = { phase: 'waiting' };
  vm.runInContext('render(snapshot)', p.context);
  assert.doesNotMatch(get('activationRequest').textContent, /已收到/);
  assert.match(get('activationNext').textContent, /游戏中请求/);
});

test('activation timeout retains backend pending lock until a completed snapshot', async () => {
  const p = page();
  p.finishBatch(); await flush();
  const button = p.document.getElementById('enableAcceleration');
  button.click();
  assert.equal(p.actionDeadlines[0].delay, 120000);
  p.expireAction(true); await flush();
  p.finishBatch(3, {}, { running: true, activation: { phase: 'hosts' } });
  await flush();
  assert.equal(button.disabled, true);
  button.click();
  assert.equal(p.requests.filter(r => r.method === 'POST').length, 1);
  p.fireTimer();
  p.finishBatch(5, {}, { running: true, hosts: { ready: true }, activation: { phase: 'waiting' } });
  await flush();
  assert.equal(button.disabled, false);
});

test('active downloads render safe candidates, unknown values, bounded progress and offline state', () => {
  const p = page(true);
  p.context.snapshot = { running: false, bytesPerSecond: 1000000, tasks: [{ id: 1, resource: 'shared', stage: 'publish', host: 'play.udon.dance', bytes: 110, size: 100, bytesPerSecond: 0, idleMS: 2000,
    songs: [{ id: '1', title: '<script>text</script>' }, { id: '2', title: '' }] }] };
  vm.runInContext('renderDownloads(snapshot)', p.context);
  const get = id => p.document.getElementById(id);
  assert.equal(get('downloadSpeed').textContent, '1.000 MB/s');
  assert.match(get('downloadState').textContent, /CDN 已关闭.*1 个活动任务/);
  assert.match(get('downloadList').textContent, /播放歌曲未确定.*<script>text<\/script>/);
  const main = get('downloadList').children[0].children[0];
  assert.match(main.textContent, /校验并发布.*CF.*100.0%/);
  assert.doesNotMatch(main.textContent, /shared|play.udon.dance|ID 2/);
  let details = get('downloadList').children[0].children.at(-1);
  assert.equal(details.open, false);
  assert.match(details.textContent, /ID 2.*play.udon.dance.*shared/);
  details.open = true;
  details.children[0].focus();
  vm.runInContext('renderDownloads(snapshot)', p.context);
  details = get('downloadList').children[0].children.at(-1);
  assert.equal(details.open, true);
  assert.equal(p.document.activeElement, details.children[0]);
  const progress = get('downloadList').children[0].children.find(e => e.tagName === 'PROGRESS');
  assert.equal(progress.value, 100);
  assert.match(progress['aria-label'], /已读取字节进度/);
  p.context.snapshot.tasks[0].host = 'nya.xin.moe';
  vm.runInContext('renderDownloads(snapshot)', p.context);
  assert.match(get('downloadList').children[0].children[0].textContent, /HKG/);
  Object.assign(p.context.snapshot.tasks[0], { songs: [], size: 0, host: '', stage: 'upstream_headers' });
  vm.runInContext('renderDownloads(snapshot)', p.context);
  assert.match(get('downloadList').textContent, /未知歌名.*等待上游.*线路待定.*进度未知/);
  p.context.snapshot.tasks = []; p.context.snapshot.bytesPerSecond = 0;
  vm.runInContext('renderDownloads(snapshot)', p.context);
  assert.equal(get('downloadList').children.length, 0);
  assert.match(get('downloadState').textContent, /暂无活动下载任务/);
});

test('active refresh authenticates, clears stale data on failure, ignores pre-save results and recovers', async () => {
  const p = page(true);
  let finish;
  p.context.fetch = (url, options) => {
    assert.equal(url, '/api/downloads');
    assert.equal(options.headers['X-StepStash-Token'], 'test-token');
    return new Promise(resolve => { finish = data => resolve({ ok: true, json: async () => data }); });
  };
  const pending = vm.runInContext('refreshDownloads()', p.context);
  vm.runInContext('settingsRevision++', p.context);
  finish({ running: true, tasks: [], bytesPerSecond: 999 });
  await pending;
  assert.notEqual(p.document.getElementById('downloadState').textContent, 'CDN 已开启 · 暂无活动下载任务');
  p.context.fetch = async () => { throw Error('offline'); };
  await vm.runInContext('refreshDownloads()', p.context);
  assert.equal(p.document.getElementById('downloadSpeed').textContent, '—');
  assert.match(p.document.getElementById('downloadState').textContent, /读取失败/);
  p.context.fetch = async () => ({ ok: true, json: async () => ({ running: true, tasks: [], bytesPerSecond: 0 }) });
  await vm.runInContext('refreshDownloads()', p.context);
  assert.match(p.document.getElementById('downloadState').textContent, /暂无活动下载任务/);
});

test('active reads share scheduler, block overlap, and pause while hidden', async () => {
  const p = page(true), original = p.context.fetch;
  let calls = 0, finish;
  p.context.fetch = (url, options) => url === '/api/downloads' ? new Promise(resolve => {
    calls++;
    finish = () => resolve({ ok: true, json: async () => ({ running: false, tasks: [], bytesPerSecond: 0 }) });
  }) : original(url, options);
  p.visibility(false);
  p.finishBatch();
  await flush();
  assert.equal(calls, 1);
  assert.equal(p.timers.size, 0);
  p.visibility(true); p.visibility(false);
  assert.equal(calls, 1);
  finish(); await flush();
  assert.equal(calls, 2);
  p.finishBatch(2); p.visibility(true);
  finish(); await flush();
  assert.equal(p.timers.size, 0);
  assert.equal(calls, 2);
});

for (const removeAuth of [false, true]) {
  test(`saved SOCKS5 password is hidden and ${removeAuth ? 'explicitly cleared with username' : 'omitted when unchanged'}`, async () => {
    const p = page();
    p.finishBatch(0, { upstreamMode: 'socks5', socks5Address: '127.0.0.1:1080', socks5Username: 'test-user' }, { socks5PasswordSet: true });
    await flush();
    const get = id => p.document.getElementById(id);
    assert.equal(get('socks5Password').value, '');
    assert.match(get('socks5Password').placeholder, /已保存/);
    if (removeAuth) get('socks5Username').value = '';
    get('settings').dispatchEvent({ type: 'change' });
    get('settings').dispatchEvent({ type: 'submit' });
    const body = JSON.parse(p.requests[2].body);
    assert.equal(Object.hasOwn(body, 'socks5Password'), removeAuth);
    if (removeAuth) assert.equal(body.socks5Password, '');
    p.requests[2].finish({ ok: true });
    await flush();
    p.finishBatch(3);
    await flush();
    assert.equal(get('socks5Password').value, '');
  });
}

test('SOCKS5 settings toggle, preserve the endpoint, and lock while running', async () => {
  const p = page();
  p.finishBatch();
  await flush();
  const get = id => p.document.getElementById(id);
  assert.equal(get('upstreamMode').value, 'direct');
  assert.equal(get('socks5Address').disabled, true);
  get('upstreamMode').value = 'socks5';
  get('settings').dispatchEvent({ type: 'change' });
  assert.equal(get('socks5Address').disabled, false);
  assert.equal(get('socks5Address').required, true);
  get('socks5Address').value = '127.0.0.1:7891';
  get('upstreamMode').value = 'direct';
  get('settings').dispatchEvent({ type: 'change' });
  assert.equal(get('socks5Address').disabled, true);
  assert.equal(get('socks5Address').value, '127.0.0.1:7891');
  get('upstreamMode').value = 'auto';
  get('settings').dispatchEvent({ type: 'change' });
  assert.equal(get('socks5Address').disabled, false);
  assert.equal(get('socks5Address').required, true);
  get('upstreamMode').value = 'socks5';
  get('settings').dispatchEvent({ type: 'change' });
  p.fireTimer();
  p.finishBatch(2, {}, { running: true });
  await flush();
  assert.equal(get('upstreamMode').disabled, true);
  assert.equal(get('socks5Address').disabled, true);
});

for (const inventoryFirst of [false, true]) {
  test(`disconnect keeps every button disabled when inventory returns ${inventoryFirst ? 'first' : 'last'}`, async () => {
    const p = page();
    p.finishBatch();
    await flush();
    p.fireTimer();
    const inventory = () => p.requests[3].finish({ bytes: 0, scanning: false });
    const disconnect = () => p.requests[2].reject(new Error('offline'));
    (inventoryFirst ? inventory : disconnect)();
    await flush();
    (inventoryFirst ? disconnect : inventory)();
    await flush();
    assert.equal(vm.runInContext("$('connection').textContent", p.context), '控制台连接中断');
    assert.equal(vm.runInContext(`document.querySelectorAll('[data-action^="hosts/"]').length`, p.context), 2);
    assert.equal(vm.runInContext("[...document.querySelectorAll('button')].every(b => b.disabled)", p.context), true);
    assert.equal(vm.runInContext("$('storageDir').disabled", p.context), true);
    p.fireTimer();
    p.finishBatch(4);
    await flush();
    assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), false);
    assert.equal(vm.runInContext("$('start').disabled", p.context), false);
    assert.equal(vm.runInContext("$('stop').disabled", p.context), true);
    assert.equal(vm.runInContext(`[...document.querySelectorAll('[data-action^="hosts/"]')].every(b => !b.disabled)`, p.context), true);
  });
}

test('inventory scan waits for connection and inventory, and respects scanning and busy state', async () => {
  const p = page();
  assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), true);
  p.requests[1].finish({ bytes: 0, scanning: false });
  await flush();
  assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), true);
  p.requests[0].finish({ settings: {}, hosts: {}, batch: {}, queue: {} });
  await flush();
  assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), false);
  p.fireTimer();
  p.requests[3].finish({ bytes: 0, scanning: true });
  await flush();
  p.requests[2].finish({ settings: {}, hosts: {}, batch: {}, queue: {} });
  await flush();
  assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), true);
  const action = vm.runInContext("action('start')", p.context);
  p.fireTimer();
  p.finishBatch(5);
  await flush();
  assert.equal(vm.runInContext("[...document.querySelectorAll('button')].every(b => b.disabled)", p.context), true);
  p.requests[4].finish();
  await flush();
  p.finishBatch(7);
  await action;
  assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), false);
});

test('unavailable inventory disables scanning until a successful inventory read', async () => {
  const p = page();
  p.requests[0].finish({ settings: {}, hosts: {}, batch: {}, queue: {} });
  await flush();
  assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), true);
  p.requests[1].reject(new Error('offline'));
  await flush();
  assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), true);
  p.fireTimer();
  p.finishBatch(2);
  await flush();
  assert.equal(vm.runInContext("$('inventoryScan').disabled", p.context), false);
});

test('untouched settings follow server changes on polling and return to a visible page', async () => {
  const p = page();
  p.finishBatch(0, { storageDir: 'D:/old' });
  await flush();
  assert.equal(vm.runInContext("$('storageDir').value", p.context), 'D:/old');
  p.fireTimer();
  p.finishBatch(2, { storageDir: 'D:/new', autoStartCDN: true, requestRetentionDays: 0,
    logDir: 'D:/logs',
    downloadUpstream: 'direct', maxCacheBytes: 2147483648 });
  await flush();
  assert.deepEqual(JSON.parse(vm.runInContext(`JSON.stringify([
    $('storageDir').value, $('autoStartCDN').checked, $('requestRetentionDays').value,
    $('logDir').value, $('downloadUpstream').value, $('maxCacheGiB').value
  ])`, p.context)), ['D:/new', true, 0, 'D:/logs', 'direct', 2]);
  p.visibility(true);
  p.visibility(false);
  p.finishBatch(4, { storageDir: 'D:/latest' });
  await flush();
  assert.equal(vm.runInContext("$('storageDir').value", p.context), 'D:/latest');
});

for (const event of ['input', 'change']) {
  test(`${event} preserves edited settings through polling and unrelated actions`, async () => {
    const p = page();
    p.finishBatch(0, { storageDir: 'D:/old' });
    await flush();
    vm.runInContext(`$('storageDir').value = 'D:/draft'; $('settings').dispatchEvent({ type: '${event}' })`, p.context);
    p.fireTimer();
    p.finishBatch(2, { storageDir: 'D:/new' });
    await flush();
    const action = vm.runInContext("action('inventory/scan')", p.context);
    p.requests[4].finish();
    await flush();
    p.finishBatch(5, { storageDir: 'D:/new' });
    await action;
    assert.equal(vm.runInContext("$('storageDir').value", p.context), 'D:/draft');
  });
}

for (const outcome of ['success', 'failure', 'lost response']) {
  test(`settings save ${outcome} ${outcome === 'success' ? 'fills effective values and resumes syncing' : 'preserves the draft'}`, async () => {
    const p = page();
    p.finishBatch(0, { storageDir: 'D:/old' });
    await flush();
    vm.runInContext("$('storageDir').value = 'relative'; $('settings').dispatchEvent({ type: 'input' })", p.context);
    p.fireTimer(); // A pre-save read remains in flight when saving completes.
    const action = vm.runInContext("action('settings', { storageDir: 'relative' })", p.context);
    if (outcome === 'lost response') p.requests[4].reject(new Error('offline'));
    else p.requests[4].finish(outcome === 'success' ? { ok: true } : { error: 'invalid' }, outcome === 'success');
    await flush();
    p.finishBatch(2, { storageDir: 'D:/old' });
    await flush();
    assert.equal(vm.runInContext("$('storageDir').value", p.context), 'relative');
    p.finishBatch(5, { storageDir: 'D:/effective/relative' });
    await action;
    assert.equal(vm.runInContext("$('storageDir').value", p.context), outcome === 'success' ? 'D:/effective/relative' : 'relative');
    p.fireTimer();
    p.finishBatch(7, { storageDir: 'D:/latest' });
    await flush();
    assert.equal(vm.runInContext("$('storageDir').value", p.context), outcome === 'success' ? 'D:/latest' : 'relative');
  });
}

function page(hidden = false) {
  const requests = [];
  const timers = new Map();
  const elements = new Map();
  const listeners = new Map();
  let nextTimer = 0;
  // Only model the DOM APIs used by the console; retain nodes and listeners so
  // rendering and user interactions can be asserted without calling action().
  function element(tagName = 'div') {
    const handlers = new Map();
    let ownText = '';
    return {
      tagName: tagName.toUpperCase(),
      dataset: {},
      children: [],
      disabled: false,
      hidden: false,
      get textContent() { return ownText + this.children.map(child => child.textContent).join(''); },
      set textContent(value) { ownText = String(value); this.children = []; },
      addEventListener(event, callback) {
        if (!handlers.has(event)) handlers.set(event, []);
        handlers.get(event).push(callback);
      },
      dispatchEvent(event) {
        event.target ??= this;
        event.preventDefault ??= () => { event.defaultPrevented = true; };
        for (const callback of handlers.get(event.type) || []) callback(event);
        return !event.defaultPrevented;
      },
      click() {
        if (!this.disabled) this.dispatchEvent({ type: 'click' });
      },
      append(...nodes) { this.children.push(...nodes); },
      setAttribute(name, value) { this[name] = value; },
      replaceChildren(...nodes) { this.children = [...nodes]; },
      contains(node) { return this === node || this.children.some(child => child.contains(node)); },
      focus() { document.activeElement = this; },
    };
  }
  const document = {
    hidden,
    querySelector: () => ({ content: 'test-token' }),
    getElementById(id) {
      return elements.get(id) || null;
    },
    createElement: tagName => element(tagName),
    querySelectorAll(selector) {
      if (selector === 'button') return buttons;
      if (selector === '[data-action]') return buttons.filter(b => b.dataset.action);
      if (selector === '[data-action^="hosts/"]')
        return buttons.filter(b => b.dataset.action?.startsWith('hosts/'));
      throw new Error(`Unsupported selector: ${selector}`);
    },
    addEventListener: (event, callback) => listeners.set(event, callback),
    dispatchEvent: event => listeners.get(event.type)?.(event),
  };
  const buttons = [];
  for (const [tag, tagName] of html.matchAll(/<([a-z][a-z0-9]*)\b[^>]*>/g)) {
    const node = element(tagName);
    const id = tag.match(/\bid="([^"]+)"/)?.[1];
    if (id) elements.set(id, node);
    const action = tag.match(/\bdata-action="([^"]+)"/)?.[1];
    if (action) node.dataset.action = action;
    if (tagName === 'button') buttons.push(node);
  }
  const context = vm.createContext({
    document,
    AbortController,
    URLSearchParams,
    fetch(url, options = {}) {
      if (url === '/api/downloads') return Promise.resolve({ ok: true, json: async () => ({ running: false, tasks: [], bytesPerSecond: 0 }) });
      if (url.startsWith('/api/requests?')) return Promise.resolve({ ok: true, json: async () => ({ storageID: 'test', requests: [], hasMore: false }) });
      const { signal } = options;
      return new Promise((resolve, reject) => {
        let rejectBody;
        signal?.addEventListener('abort', () => {
          reject(signal.reason);
          rejectBody?.(signal.reason);
        }, { once: true });
        requests.push({
          url, ...options, reject,
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
    requests, context, document,
    get timers() { return new Map([...timers].filter(([, timer]) => timer.delay === 5000)); },
    get deadlines() { return new Map([...timers].filter(([, timer]) => timer.delay === 10000)); },
    visibility(hidden) {
      document.hidden = hidden;
      listeners.get('visibilitychange')();
    },
    finishBatch(start = 0, settings = {}, state = {}) {
      requests[start].finish({ settings, hosts: {}, batch: {}, queue: {}, ...state });
      requests[start + 1].finish({ bytes: 0 });
    },
    fireTimer: () => fireTimer(5000),
    expireRead: () => fireTimer(10000),
    expireAction: hosts => fireTimer(hosts ? 120000 : 30000),
    get actionDeadlines() { return [...timers.values()].filter(t => [30000, 120000].includes(t.delay)); },
  };
}

test('nonempty queue renders three songs in order, falls back to IDs, and replaces stale nodes', async () => {
  const p = page();
  const get = id => p.document.getElementById(id);
  const title = '<img src=x onerror=alert(1)>';
  p.finishBatch(0, {}, { queue: {
    running: true, current: '11', active: ['11', '12'], completed: 2,
    file: 'output_log.txt', logError: '日志不可读', error: '下载失败',
    songs: [{ songId: 11, title }, { songId: 12 }, { songId: 13, title: '第三首' }, { songId: 14, title: '第四首' }],
  } });
  await flush();
  assert.equal(get('connection').textContent, '● 控制台已连接');
  assert.deepEqual(get('queueSongs').children.map(node => [node.tagName, node.textContent, node.children.length]),
    [['LI', title, 0], ['LI', '12', 0], ['LI', '第三首', 0]]);
  assert.equal(get('queuePhase').textContent, '正在准备歌曲 11、12');
  assert.equal(get('queueDetail').textContent, 'output_log.txt · 本次开启后累计准备成功 2 次');
  assert.equal(get('queueError').textContent, '日志不可读');
  assert.equal(get('batchStart').textContent, '暂停预缓存并下载补齐');

  p.fireTimer();
  p.finishBatch(2, {}, { queue: { running: true, songs: [{ songId: 15 }], error: '下载失败' } });
  await flush();
  assert.deepEqual(get('queueSongs').children.map(node => node.textContent), ['15']);
  assert.equal(get('queuePhase').textContent, '等待队列变化或重试');
  assert.equal(get('queueError').textContent, '下载失败');

  p.fireTimer();
  p.finishBatch(4);
  await flush();
  assert.deepEqual(get('queueSongs').children, []);
  assert.equal(get('queueError').textContent, '');
  assert.equal(get('queuePhase').textContent, '随本地 CDN 启动');
});

test('batch failures render rows as text and disappear after a successful refresh', async () => {
  const p = page();
  const get = id => p.document.getElementById(id);
  const failures = [
    { id: 11, name: '<b>歌曲</b>', error: '<script>alert(1)</script>' },
    { id: 12, name: '另一首', error: '校验失败' },
  ];
  p.finishBatch(0, {}, { batch: {
    running: true, scanOnly: true, total: 4, checked: 3, hits: 1,
    missing: 2, downloaded: 0, failed: 2, failures, phase: '扫描中', current: '另一首',
  } });
  await flush();
  assert.equal(get('connection').textContent, '● 控制台已连接');
  assert.equal(get('failures').hidden, false);
  assert.deepEqual(get('failureRows').children.map(row => [row.tagName,
    row.children.map(cell => [cell.tagName, cell.textContent, cell.children.length])]), [
    ['TR', [['TD', '11 · <b>歌曲</b>', 0], ['TD', '<script>alert(1)</script>', 0]]],
    ['TR', [['TD', '12 · 另一首', 0], ['TD', '校验失败', 0]]],
  ]);
  assert.equal(get('progress').max, 4);
  assert.equal(get('progress').value, 3);
  assert.equal(get('phase').textContent, '扫描中');
  assert.equal(get('current').textContent, '另一首 · 当前任务 3 / 4 · 命中 1 · 缺失 2 · 本地文件命中 0 · 下载完成 0 · 失败 2');
  assert.equal(get('batchScan').disabled, true);
  assert.equal(get('batchScan').textContent, '正在扫描…');
  assert.equal(get('batchCancel').disabled, false);

  p.fireTimer();
  p.finishBatch(2, {}, { batch: { failed: 1, failures: failures.slice(1) } });
  await flush();
  assert.equal(get('failureRows').children.length, 1);
  assert.equal(get('failureRows').children[0].children[0].textContent, '12 · 另一首');
  p.fireTimer();
  p.finishBatch(4);
  await flush();
  assert.equal(get('failures').hidden, true);
  assert.deepEqual(get('failureRows').children, []);
  assert.equal(get('batchScan').disabled, false);
  assert.equal(get('batchCancel').disabled, true);
});

for (const [path, state] of [
  ['activation/enable', {}],
  ['start', {}], ['stop', { running: true }],
  ['hosts/enable', {}], ['hosts/disable', {}], ['inventory/scan', {}],
  ['batch/scan', {}], ['batch/switch', {}], ['batch/cancel', { batch: { running: true } }],
]) {
  test(`clicking the ${path} button posts once and refreshes before unlocking controls`, async () => {
    const p = page();
    const button = p.document.querySelectorAll('[data-action]').find(b => b.dataset.action === path);
    assert.ok(button, `Missing button for ${path}`);
    button.click(); // Disabled while the initial state is unknown.
    assert.equal(p.requests.length, 2);
    p.finishBatch(0, {}, state);
    await flush();
    assert.equal(button.disabled, false);
    button.click();
    assert.equal(p.requests.length, 3);
    const request = p.requests[2];
    assert.equal(request.url, '/api/' + path);
    assert.equal(request.method, 'POST');
    assert.equal(request.headers['X-StepStash-Token'], 'test-token');
    assert.equal(request.headers['Content-Type'], 'application/json');
    assert.deepEqual(JSON.parse(request.body), {});
    assert.equal(p.document.querySelectorAll('button').every(b => b.disabled), true);
    button.click();
    assert.equal(p.requests.length, 3);
    request.finish({ ok: true });
    await flush();
    assert.deepEqual(p.requests.slice(3).map(r => r.url), ['/api/status', '/api/inventory']);
    p.finishBatch(3, {}, state);
    await flush();
    assert.equal(button.disabled, false);
    assert.equal(p.document.getElementById('notice').hidden, false);
    assert.match(p.document.getElementById('notice').textContent, /已启动|已完成|已移除/);
    assert.equal(p.timers.size, 1);
  });
}

for (const lostResponse of [false, true]) {
  test(`button click recovers after ${lostResponse ? 'a lost response' : 'a rejected action'} without resubmitting`, async () => {
    const p = page();
    const get = id => p.document.getElementById(id);
    p.finishBatch();
    await flush();
    get('start').click();
    assert.equal(p.requests[2].url, '/api/start');
    if (lostResponse) p.requests[2].reject(new Error('offline'));
    else p.requests[2].finish({ error: '端口已占用' }, false);
    await flush();
    assert.deepEqual(p.requests.slice(3).map(r => r.url), ['/api/status', '/api/inventory']);
    assert.equal(get('start').disabled, true);
    p.finishBatch(3);
    await flush();
    assert.equal(get('start').disabled, false);
    assert.equal(get('notice').hidden, false);
    assert.match(get('notice').textContent, lostResponse ? /结果尚未确认.*已刷新当前状态/s : /端口已占用/);
    assert.equal(p.requests.filter(r => r.method === 'POST').length, 1);
    assert.equal(p.actionDeadlines.length, 0);
    assert.equal(p.timers.size, 1);
  });
}

test('settings submit prevents navigation and serializes the edited controls', async () => {
  const p = page();
  p.finishBatch();
  await flush();
  const get = id => p.document.getElementById(id);
  const values = {
    queuePrefetchCount: '7',
    storageDir: 'D:/draft', logDir: 'D:/logs', downloadUpstream: 'hkg',
    upstreamMode: 'socks5', socks5Address: '127.0.0.1:7891',
    socks5Username: 'test-user', socks5Password: 'test-secret',
    requestRetentionDays: '0', maxCacheGiB: '1.25',
  };
  for (const [id, value] of Object.entries(values)) get(id).value = value;
  get('manualLogDir').checked = true;
  get('autoStartCDN').checked = true;
  get('settings').dispatchEvent({ type: 'input' });
  const event = { type: 'submit' };
  assert.equal(get('settings').dispatchEvent(event), false);
  assert.equal(event.defaultPrevented, true);
  assert.equal(p.requests[2].url, '/api/settings');
  assert.equal(p.requests[2].method, 'POST');
  assert.deepEqual(JSON.parse(p.requests[2].body), {
    queuePrefetchCount: 7, queuePrefetchEnabled: true,
    autoStartCDN: true, storageDir: 'D:/draft', manualLogDir: true, logDir: 'D:/logs', downloadUpstream: 'hkg',
    upstreamMode: 'socks5', socks5Address: '127.0.0.1:7891',
    socks5Username: 'test-user', socks5Password: 'test-secret',
    requestRetentionDays: 0, maxCacheBytes: 1342177280,
  });
  assert.equal(get('save').disabled, true);
  p.requests[2].finish({ ok: true });
  await flush();
  p.finishBatch(3, { storageDir: 'D:/effective' });
  await flush();
  assert.equal(get('storageDir').value, 'D:/effective');
  assert.equal(get('save').disabled, false);
  assert.equal(get('notice').textContent, '设置已保存，将用于下一次启动的服务或任务。');
});

for (const count of [1, 5]) {
  test(`queue displays the configured ${count} positions and locks settings while running`, async () => {
    const p = page();
    p.finishBatch(0, { queuePrefetchCount: count }, { running: true, queue: {
      running: true, songs: Array.from({ length: 6 }, (_, i) => ({ songId: i + 1 })),
    } });
    await flush();
    const get = id => p.document.getElementById(id);
    assert.equal(get('queuePrefetchCount').value, count);
    assert.equal(get('queuePrefetchCount').disabled, true);
    assert.equal(get('queueSongs').children.length, count);
    assert.equal(get('queueWindow').textContent, `准备队列前 ${count} 个位置中的有效曲目`);
  });
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
  assert.match(vm.runInContext("$('inventoryState').textContent", p.context), /请点击「刷新覆盖率」/);
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
      assert.equal(p.deadlines.size, 4);
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


test('coverage displays song ratio and does not interpret legacy inventory as zero', async () => {
  const p = page();
  p.requests[0].finish({ settings: {}, hosts: {}, batch: {}, queue: {} });
  p.requests[1].finish({ updated: '2026-09-26T12:00:00Z', bytes: 10, videos: 3 });
  await flush();
  assert.equal(vm.runInContext("$('coverageRate').textContent", p.context), '—');
  p.fireTimer();
  p.requests[2].finish({ settings: {}, hosts: {}, batch: {}, queue: {} });
  p.requests[3].finish({ updated: '2026-09-26T12:00:00Z', bytes: 10, videos: 3, coverageKnown: true, coveredSongs: 2, totalSongs: 5 });
  await flush();
  assert.equal(vm.runInContext("$('coverageRate').textContent", p.context), '40.0%');
  assert.equal(vm.runInContext("$('coverageCount').textContent", p.context), '2 / 5 首曲目已覆盖');
});

test('identical polling snapshots do not rewrite live status text', async () => {
  const p = page();
  p.finishBatch();
  await flush();
  const ids = ['connection', 'phase', 'queuePhase', 'settingsAvailability', 'serviceError', 'settingsActionError', 'hostsActionError', 'queueActionError', 'batchActionError', 'inventoryActionError'];
  let writes = 0;
  for (const id of ids) {
    const node = p.document.getElementById(id);
    let value = node.textContent;
    Object.defineProperty(node, 'textContent', {
      get: () => value,
      set: next => { value = next; writes++; },
    });
  }
  p.fireTimer();
  p.finishBatch(2);
  await flush();
  assert.equal(writes, 0);
  p.fireTimer();
  p.finishBatch(4, {}, { running: true });
  await flush();
  assert.match(p.document.getElementById('settingsAvailability').textContent, /设置已锁定/);
  assert.equal(writes, 2);
});

for (const moved of [false, true]) {
  test(`action completion ${moved ? 'respects a focus move' : 'restores focus lost when disabling its button'}`, async () => {
    const p = page();
    p.finishBatch();
    await flush();
    const origin = p.document.getElementById('save');
    let focused = 0;
    origin.focus = () => focused++;
    p.document.body = {};
    p.document.activeElement = origin;
    const action = vm.runInContext("action('settings', {})", p.context);
    p.document.activeElement = moved ? p.document.getElementById('storageDir') : p.document.body;
    p.requests[2].finish({ ok: true });
    await flush();
    p.finishBatch(3);
    await action;
    assert.equal(focused, moved ? 0 : 1);
  });
}

const recentEvent = (id, at, extra = {}) => ({ id, at, resource: 'shared', method: 'GET', range: 'bytes=0-9', cache: 'HIT', outcome: 'completed', bytes: 10, elapsedMS: 1000, status: 206, songs: [], ...extra });

test('recent groups respect resource, 30-second boundary, HEAD and full-request boundaries', () => {
  const p = page(true);
  p.context.samples = [recentEvent(7, 100000), recentEvent(6, 90000, { resource: 'other' }), recentEvent(5, 70000), recentEvent(4, 39999), recentEvent(3, 39000, { method: 'HEAD' }), recentEvent(2, 38000, { range: '' }), recentEvent(1, 37000)];
  const groups = vm.runInContext('groupRequests(samples)', p.context);
  assert.deepEqual(Array.from(groups, g => Array.from(g.requests, r => r.id)), [[7, 5], [6], [4], [3], [2], [1]]);
});

test('recent groups retain mixed results and estimate span without summing parallel durations', () => {
  const p = page(true);
  p.context.samples = [recentEvent(2, 2000, { bytes: 5, outcome: 'canceled', cache: 'MISS', elapsedMS: 4000 }), recentEvent(1, 1000, { elapsedMS: 4000 })];
  const group = vm.runInContext('groupRequests(samples)[0]', p.context);
  assert.equal(group.end - group.first, 5000);
  assert.equal(group.bytes, 15);
  assert.match(vm.runInContext('recentSummary(groupRequests(samples)[0]).textContent', p.context), /中断 ×1.*完成 ×1/);
  assert.match(vm.runInContext('recentSummary(groupRequests(samples)[0]).textContent', p.context), /回源 ×1.*命中 ×1/);
  for (const [outcome, status, pattern] of [['failed', 200, /连接中断/], ['failed', 502, /请求失败/], ['aborted', 200, /中止/], ['incomplete', 200, /不完整/]]) {
    p.context.sample = recentEvent(1, 0, { outcome, status });
    assert.match(vm.runInContext('requestOutcome(sample)', p.context), pattern);
  }
});

test('recent rendering preserves expanded groups and focus across updates, safely labels all song candidates', () => {
  const p = page(true);
  p.context.snapshot = { storageID: 'one', hasMore: true, requests: [recentEvent(1, 1000)] };
  vm.runInContext('renderRecent(snapshot)', p.context);
  const list = p.document.getElementById('recentList');
  const original = list.children[0];
  original.open = true;
  p.document.activeElement = original.children[0];
  assert.match(original.children[0].textContent, /未知歌名/);
  assert.doesNotMatch(original.children[0].textContent, /shared|bytes=/);
  assert.match(original.children[1].textContent, /资源标识：shared/);
  assert.doesNotMatch(original.children[0].textContent, /估算/);
  vm.runInContext('renderRecent(snapshot)', p.context);
  assert.equal(list.children[0], original);
  p.context.snapshot.requests.unshift(recentEvent(2, 2000, { songs: [{ id: '1', title: '<script>bad</script>' }, { id: '2', title: '' }] }));
  vm.runInContext('renderRecent(snapshot)', p.context);
  assert.equal(list.children[0].open, true);
  assert.match(list.children[0].children[0].textContent, /估算跨度2\.000 秒估算平均速度/);
  assert.doesNotMatch(list.children[0].children[1].textContent, /估算/);
  assert.equal(p.document.activeElement, list.children[0].children[0]);
  assert.match(list.children[0].children[0].textContent, /<script>bad<\/script>.*未知歌名.*播放歌曲未确定/);
  assert.equal(list.children[0].children[0].children[0].children[0].children.length, 0);
  assert.equal(list.children[0].children[0].children.length, 5);
  p.context.snapshot.storageID = 'two';
  vm.runInContext('renderRecent(snapshot)', p.context);
  assert.equal(list.children[0].open, false);
});

test('recent refresh authenticates, rejects pre-save responses, clears failures and recovers', async () => {
  const p = page(true);
  let finish, observed;
  p.context.fetch = (url, options) => { observed = { url, options }; return new Promise(resolve => { finish = value => resolve({ ok: true, json: async () => value }); }); };
  const pending = vm.runInContext('refreshRecent()', p.context);
  assert.equal(observed.options.headers['X-StepStash-Token'], 'test-token');
  assert.equal(observed.url, '/api/requests?limit=50');
  vm.runInContext('settingsRevision++; resetRecent()', p.context);
  finish({ storageID: 'old', requests: [recentEvent(1, 0)] });
  await pending;
  assert.equal(p.document.getElementById('recentList').children.length, 0);
  p.context.fetch = async () => { throw Error('unreadable'); };
  await vm.runInContext('refreshRecent()', p.context);
  assert.match(p.document.getElementById('recentState').textContent, /读取失败/);
  p.context.fetch = async () => ({ ok: true, json: async () => ({ storageID: 'new', requests: [], hasMore: false }) });
  await vm.runInContext('refreshRecent()', p.context);
  assert.match(p.document.getElementById('recentState').textContent, /暂无/);
});

test('recent load-more uses bounded expanding window and shared nonoverlapping poll scheduler', async () => {
  const p = page();
  p.finishBatch();
  await flush();
  const originalFetch = p.context.fetch;
  const recent = [];
  let finish;
  p.context.fetch = (url, options) => url.startsWith('/api/requests?') ? new Promise(resolve => {
    recent.push(url);
    finish = () => resolve({ ok: true, json: async () => ({ storageID: 'test', requests: [], hasMore: true }) });
  }) : originalFetch(url, options);
  p.document.getElementById('recentMore').click();
  assert.deepEqual(recent, ['/api/requests?limit=100']);
  p.finishBatch(2);
  await flush();
  assert.equal(p.timers.size, 0);
  p.visibility(true);
  p.visibility(false);
  assert.equal(recent.length, 1);
  finish();
  await flush();
  assert.equal(recent.length, 2);
  p.finishBatch(4);
  finish();
  await flush();
  assert.equal(p.timers.size, 1);
  vm.runInContext('recentLimit = 500', p.context);
  p.document.getElementById('recentMore').click();
  assert.equal(recent.at(-1), '/api/requests?limit=500');
  p.finishBatch(6);
  finish();
  await flush();
  assert.equal(p.document.getElementById('recentMore').hidden, true);
});

test('recent summary keeps names, badges and numbers in separate cells; fingerprints and all candidates stay in details', () => {
  const p = page(true);
  p.context.snapshot = { storageID: 'test', hasMore: false, requests: [recentEvent(1, 1000, {
    resource: 'private-resource-fingerprint', songs: [
      { id: '1', title: '第一首' }, { id: '2', title: '第二首' }, { id: '3', title: '第三首' },
    ],
  })] };
  vm.runInContext('renderRecent(snapshot)', p.context);
  const [summary, detail] = p.document.getElementById('recentList').children[0].children;
  assert.equal(summary.children.length, 5);
  assert.match(summary.children[0].textContent, /第一首.*第二首.*等 3 首/);
  assert.doesNotMatch(summary.textContent, /第三首|private-resource-fingerprint|bytes=/);
  assert.match(summary.children[1].textContent, /缓存命中 ×1.*请求处理完成 ×1/);
  assert.equal(summary.children[2].textContent, '实际传输0.000 MB');
  assert.equal(summary.children[3].textContent, '耗时1.000 秒');
  assert.equal(summary.children[4].textContent, '平均速度0.000 MB/s');
  assert.match(detail.textContent, /第三首.*private-resource-fingerprint.*bytes=0-9/);
});

function cacheHarness() {
  const p = page(true);
  vm.runInContext(cacheScript, p.context);
  vm.runInContext('connected = true', p.context);
  p.context.cacheFixture = { storageID: 'store', total: 2, entries: [
    { key: 'a', stamp: 'one', known: true, protected: false, bytes: 1073741824, lastRequest: 1000, state: '文件存在 · 完整性未检查', songCount: 2, songs: [{ id: '1', title: '<b>舞曲</b>', current: true }, { id: '2', title: '共享歌曲', current: false }] },
    { key: 'b', stamp: 'two', known: true, protected: true, bytes: 200, lastRequest: 0, state: '文件存在 · 完整性未检查', songCount: 0, songs: [] },
  ] };
  vm.runInContext('renderCache(cacheFixture)', p.context);
  return p;
}

test('cache management shows honest state and sharing; protected files remain disabled across polling', () => {
  const p = cacheHarness(), get = id => p.document.getElementById(id);
  assert.match(get('cacheList').textContent, /<b>舞曲<\/b>.*完整性未检查.*共享文件.*历史关联/s);
  assert.equal(vm.runInContext('cacheRows[1].check.disabled', p.context), true);
  get('cacheSelect').click();
  assert.equal(vm.runInContext('cacheChosen().length', p.context), 1);
  vm.runInContext('renderControls()', p.context);
  assert.equal(vm.runInContext('cacheRows[1].check.disabled', p.context), true);
  get('cacheDelete').click();
  assert.equal(get('cacheConfirm').hidden, false);
  assert.match(get('cacheConfirmList').textContent, /影响 2 首/);
  assert.equal(p.document.activeElement, get('cacheCancel'));
  get('cacheCancel').click();
  assert.equal(get('cacheConfirm').hidden, true);
  assert.equal(p.document.activeElement, get('cacheDelete'));
});

test('cache controls and action entry points stay locked during remote or timed-out activation', async () => {
  const p = cacheHarness(), get = id => p.document.getElementById(id), calls = [];
  p.context.fetch = async url => { calls.push(url); return { ok: true, json: async () => p.context.cacheFixture }; };
  get('cacheSelect').click(); get('cacheDelete').click();
  for (const phase of ['checking', 'starting', 'hosts']) {
    p.context.phase = phase;
    vm.runInContext('busy = false; lastState = { activation: { phase }, settings: {}, batch: {}, queue: {} }; renderControls(); updateCacheControls()', p.context);
    for (const id of ['cacheRefresh', 'cacheSearchButton', 'cacheOpen', 'cacheSelect', 'cacheDelete', 'cachePrev', 'cacheNext', 'cacheConfirmDelete', 'cacheCancel']) {
      assert.equal(get(id).disabled, true, `${phase}: ${id}`);
      get(id).click();
    }
    assert.equal(vm.runInContext('cacheRows.every(r => r.check.disabled && r.locate.disabled)', p.context), true);
    get('cacheSearchForm').dispatchEvent({ type: 'submit' });
    await vm.runInContext("cacheAction('open', []); cacheAction('delete', cachePending); refreshCache()", p.context);
    assert.equal(calls.length, 0);
  }
  vm.runInContext("lastState.activation.phase = 'waiting'; renderControls()", p.context);
  assert.equal(get('cacheRefresh').disabled, false);
  assert.equal(get('cacheConfirmDelete').disabled, false);
  assert.equal(vm.runInContext('cacheRows[0].locate.disabled', p.context), false);
  assert.equal(vm.runInContext('cacheRows[1].check.disabled', p.context), true);
  await vm.runInContext('refreshCache()', p.context);
  assert.equal(calls.length, 1);
});

test('cache deletion sends only confirmed identities, reports protected outcomes, and refreshes', async () => {
  const p = cacheHarness(), calls = [], get = id => p.document.getElementById(id);
  p.context.fetch = async (url, options) => {
    calls.push({ url, options });
    if (options.method === 'POST') return { ok: true, json: async () => [{ key: 'a', result: 'protected' }] };
    return { ok: true, json: async () => p.context.cacheFixture };
  };
  get('cacheSelect').click(); get('cacheDelete').click();
  assert.equal(calls.length, 0);
  get('cacheConfirmDelete').click();
  await flush(); await flush();
  assert.equal(calls[0].url, '/api/cache/delete');
  assert.equal(calls[0].options.headers['X-StepStash-Token'], 'test-token');
  assert.deepEqual(JSON.parse(calls[0].options.body), { storageID: 'store', entries: [{ key: 'a', stamp: 'one' }] });
  assert.match(get('cacheResult').textContent, /受保护，已跳过/);
  assert.equal(calls.length, 2);
  assert.equal(get('cacheConfirm').hidden, true);
  assert.equal(vm.runInContext('cacheChosen().length', p.context), 0);
});

test('cache refresh rejects stale storage responses and clears selection on errors', async () => {
  const p = cacheHarness();
  let finish;
  p.context.fetch = () => new Promise(resolve => { finish = data => resolve({ ok: true, json: async () => data }); });
  const pending = vm.runInContext('refreshCache()', p.context);
  vm.runInContext('settingsRevision++; resetCache()', p.context);
  finish(p.context.cacheFixture); await pending;
  assert.equal(p.document.getElementById('cacheList').children.length, 0);
  p.context.fetch = async () => { throw Error('failed'); };
  await vm.runInContext('refreshCache()', p.context);
  assert.match(p.document.getElementById('cacheState').textContent, /读取失败/);
  assert.equal(p.document.getElementById('cacheDelete').disabled, true);
});

test('deleting the last cache page returns to the last valid page with the same filters', async () => {
  const p = cacheHarness(), offsets = [], get = id => p.document.getElementById(id);
  get('cacheSearch').value = '舞曲'; get('cacheSort').value = 'size';
  vm.runInContext('cacheOffset = 50', p.context);
  p.context.fetch = async (url, options) => {
    if (options.method === 'POST') return { ok: true, json: async () => [{ key: 'a', result: 'deleted' }] };
    const query = new URL(url, 'http://localhost').searchParams;
    assert.equal(query.get('q'), '舞曲'); assert.equal(query.get('sort'), 'size');
    offsets.push(query.get('offset'));
    return { ok: true, json: async () => ({ storageID: 'store', total: 50, entries: query.get('offset') === '50' ? [] : [p.context.cacheFixture.entries[0]] }) };
  };
  await vm.runInContext("cacheAction('delete', [cacheFixture.entries[0]])", p.context);
  assert.deepEqual(offsets, ['50', '0']);
  assert.match(get('cacheState').textContent, /第 1 页.*显示 1 项/);
  assert.match(get('cacheResult').textContent, /已删除/);
  assert.equal(get('cachePrev').disabled, true);
  assert.equal(vm.runInContext('cacheChosen().length', p.context), 0);
});

test('cache pagination handles an emptied store and bounds retries during concurrent shrink', async () => {
  const p = cacheHarness(), offsets = [];
  vm.runInContext('cacheOffset = 150', p.context);
  p.context.fetch = async url => {
    const offset = new URL(url, 'http://localhost').searchParams.get('offset'); offsets.push(offset);
    return { ok: true, json: async () => ({ storageID: 'store', total: offsets.length === 1 ? 120 : offsets.length === 2 ? 60 : 0, entries: [] }) };
  };
  await vm.runInContext('refreshCache()', p.context);
  assert.deepEqual(offsets, ['150', '100', '0']);
  assert.equal(vm.runInContext('cacheOffset', p.context), 0);
  assert.match(p.document.getElementById('cacheState').textContent, /没有匹配/);
  offsets.length = 0;
  vm.runInContext('cacheOffset = 50', p.context);
  p.context.fetch = async url => { offsets.push(url); return { ok: true, json: async () => ({ storageID: 'store', total: 0, entries: [] }) }; };
  await vm.runInContext('refreshCache()', p.context);
  assert.equal(offsets.length, 1);
  assert.equal(vm.runInContext('cacheOffset', p.context), 0);
});

for (const enabled of [false, true]) {
 test('queue preference renders and saves ' + enabled, async () => {
  const p = page();
  p.finishBatch(0, { queuePrefetchEnabled: enabled });
  await flush();
  const get = id => p.document.getElementById(id);
  assert.equal(get('queuePrefetchEnabled').checked, enabled);
  assert.equal(get('queuePrefetchEnabled').disabled, false);
  assert.equal(get('queuePhase').textContent, enabled ? '随本地 CDN 启动' : '已在设置中关闭随 CDN 启用');
  get('queuePrefetchEnabled').checked = !enabled;
  get('settings').dispatchEvent({ type: 'input' });
  get('settings').dispatchEvent({ type: 'submit' });
  assert.equal(JSON.parse(p.requests[2].body).queuePrefetchEnabled, !enabled);
 });
}

test('queue cannot independently lock settings when CDN is stopped', async () => {
 const p = page();
 p.finishBatch(0, {}, { running: false, queue: { running: true } });
 await flush();
 assert.equal(p.document.getElementById('save').disabled, false);
 assert.equal(p.document.getElementById('queuePrefetchEnabled').disabled, false);
});

test('log directory requires explicit manual selection and resets to automatic', async () => {
 const p = page();
 p.finishBatch(0, { logDir: 'current-user/logs', manualLogDir: false }, { defaultLogDir: 'current-user/logs' });
 await flush();
 const get = id => p.document.getElementById(id);
 assert.equal(get('manualLogDir').checked, false);
 assert.equal(get('logDir').disabled, true);
 assert.equal(get('logDir').value, 'current-user/logs');
 get('manualLogDir').checked = true;
 get('settings').dispatchEvent({ type: 'change' });
 assert.equal(get('logDir').disabled, false);
 assert.equal(get('logDir').required, true);
 get('logDir').value = 'custom/logs';
 get('manualLogDir').checked = false;
 get('settings').dispatchEvent({ type: 'change' });
 assert.equal(get('logDir').disabled, true);
 assert.equal(get('logDir').value, 'current-user/logs');
 get('settings').dispatchEvent({ type: 'submit' });
 const body = JSON.parse(p.requests[2].body);
 assert.equal(body.manualLogDir, false);
 assert.equal(body.logDir, '');
});

test('manual log directory loads and submits the explicit path', async () => {
 const p = page();
 p.finishBatch(0, { logDir: 'custom/logs', manualLogDir: true }, { defaultLogDir: 'current-user/logs' });
 await flush();
 const get = id => p.document.getElementById(id);
 assert.equal(get('manualLogDir').checked, true);
 assert.equal(get('logDir').disabled, false);
 assert.equal(get('logDir').value, 'custom/logs');
 get('settings').dispatchEvent({ type: 'submit' });
 const body = JSON.parse(p.requests[2].body);
 assert.equal(body.manualLogDir, true);
 assert.equal(body.logDir, 'custom/logs');
});

test('paused monitor rejects in-flight results and errors, and resumes fresh reads', async () => {
  for (const fail of [false, true]) {
    const p = page(true), get = id => p.document.getElementById(id);
    let finish;
    p.context.fetch = () => new Promise((resolve, reject) => { finish = () => fail ? reject(Error('offline')) : resolve({ ok: true, json: async () => ({ tasks: [], bytesPerSecond: 9000000 }) }); });
    get('downloadSpeed').textContent = 'frozen';
    const pending = vm.runInContext('refreshDownloads()', p.context);
    get('pauseMonitor').checked = true;
    get('pauseMonitor').dispatchEvent({ type: 'change' });
    finish(); await pending;
    assert.equal(get('downloadSpeed').textContent, 'frozen');
    vm.runInContext('renderControls()', p.context);
    assert.equal(get('recentMore').disabled, true);
    assert.match(get('monitorRefreshState').textContent, /已暂停/);
    get('pauseMonitor').checked = false;
    get('pauseMonitor').dispatchEvent({ type: 'change' });
    p.context.fetch = async () => ({ ok: true, json: async () => ({ tasks: [], bytesPerSecond: 2000000 }) });
    await vm.runInContext('refreshDownloads()', p.context);
    assert.equal(get('downloadSpeed').textContent, '2.000 MB/s');
  }
});

test('monitor list removal and failures retain keyboard focus in their section', async () => {
  for (const kind of ['download', 'recent']) {
    for (const failure of [false, true]) {
      const p = page(true), get = id => p.document.getElementById(id);
      p.context.snapshot = kind === 'download'
        ? { tasks: [{ id: 1, resource: 'fixture', songs: [], size: 0, bytes: 0, bytesPerSecond: 0, idleMS: 0 }], bytesPerSecond: 0 }
        : { storageID: 'test', requests: [recentEvent(1, 1000)], hasMore: false };
      vm.runInContext(kind === 'download' ? 'renderDownloads(snapshot)' : 'renderRecent(snapshot)', p.context);
      const list = get(kind === 'download' ? 'downloadList' : 'recentList');
      const summary = kind === 'download' ? list.children[0].children.at(-1).children[0] : list.children[0].children[0];
      summary.focus();
      if (failure) {
        p.context.fetch = async () => { throw Error('offline'); };
        await vm.runInContext(kind === 'download' ? 'refreshDownloads()' : 'refreshRecent()', p.context);
      } else {
        p.context.snapshot.tasks = []; p.context.snapshot.requests = [];
        vm.runInContext(kind === 'download' ? 'renderDownloads(snapshot)' : 'renderRecent(snapshot)', p.context);
      }
      assert.equal(p.document.activeElement, get(kind === 'download' ? 'downloads' : 'recent'));
    }
  }
});

test('paused monitor skips both monitor endpoints while service status keeps polling', async () => {
  const p = page(true), urls = [];
  p.context.fetch = async url => {
    urls.push(url);
    return { ok: true, json: async () => url === '/api/status' ? { settings: {}, hosts: {}, batch: {}, queue: {} }
      : url === '/api/downloads' ? { tasks: [], bytesPerSecond: 0 }
      : { storageID: 'test', requests: [], hasMore: false } };
  };
  selectPage(p, 'monitor');
  const pause = p.document.getElementById('pauseMonitor');
  pause.checked = true; pause.dispatchEvent({ type: 'change' });
  p.visibility(false); await flush();
  assert.deepEqual(urls, ['/api/status']);
  p.fireTimer(); await flush();
  assert.deepEqual(urls, ['/api/status', '/api/status']);
  pause.checked = false; pause.dispatchEvent({ type: 'change' }); await flush();
  assert.deepEqual(urls.slice(2).sort(), ['/api/status', '/api/downloads', '/api/requests?limit=50'].sort());
});

test('pause discards stale recent responses even after a quick resume', async () => {
  for (const fail of [false, true]) {
    const p = page(true), get = id => p.document.getElementById(id);
    let finish;
    p.context.fetch = () => new Promise((resolve, reject) => { finish = () => fail ? reject(Error('offline')) : resolve({ ok: true, json: async () => ({ storageID: 'test', requests: [], hasMore: false }) }); });
    get('recentState').textContent = 'frozen';
    const pending = vm.runInContext('refreshRecent()', p.context);
    get('pauseMonitor').checked = true; get('pauseMonitor').dispatchEvent({ type: 'change' });
    get('pauseMonitor').checked = false; get('pauseMonitor').dispatchEvent({ type: 'change' });
    finish(); await pending;
    assert.equal(get('recentState').textContent, 'frozen');
  }
});
