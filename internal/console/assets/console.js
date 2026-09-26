const token = document.querySelector('meta[name="stepstash-token"]').content;
const $ = (id) => document.getElementById(id);
let settingsDirty = false,
  settingsRevision = 0,
  busy = false,
  connected = false,
  lastState = null,
  lastInventory = null,
  uncertainAction = '';
// Avoid replacing live-region text on identical polling snapshots.
function setText(id, value) {
  const node = $(id);
  const text = String(value ?? '');
  if (node.textContent !== text) node.textContent = text;
}
function notice(text) {
  setText('notice', text);
  $('notice').hidden = !text;
}
function render(s) {
  lastState = s;
  setText('connection', '● 控制台已连接');
  $('connection').className = 'good';
  setText('serviceBadge', s.running ? '● CDN 运行中' : '○ CDN 已关闭');
  $('serviceBadge').className = 'pill' + (s.running ? ' good' : '');
  renderTraffic(s.traffic || {});
  renderService(s);
  renderSettings(s);
  renderControls();
  renderQueue(s);
  renderBatch(s);
}

function renderTraffic(t) {
  const ms = (v) => (v == null ? '—' : v.toFixed(1) + ' ms');
  setText('trafficHits', t.hits || 0);
  setText('trafficRate', t.hitRate == null ? '—' : t.hitRate.toFixed(1) + '%');
  setText('trafficSaved', ((t.savedBytes || 0) / 1073741824).toFixed(3) + ' GiB');
  setText('trafficReduction', t.reductionPercent == null ? '—' : t.reductionPercent.toFixed(1) + '%');
  setText('trafficDetail', '成功请求 ' +
    (t.requests || 0) +
    ' 次 · 未命中 ' +
    (t.misses || 0) +
    ' 次 · 上游平均响应 ' +
    ms(t.upstreamMS) +
    '（' +
    (t.upstreamSamples || 0) +
    ' 个样本）· 本地命中平均响应 ' +
    ms(t.localMS) +
    '（' +
    (t.localSamples || 0) +
    ' 个样本）' + (t.error ? ' · ' + t.error : ''));
}

function renderService(s) {
  setText('portText', s.running
    ? 'StepStash 正在监听'
    : s.portOK
      ? '端口可用，可以启动'
      : s.portOwner
        ? '被 ' +
          s.portOwner.name +
          '（PID ' +
          s.portOwner.pid +
          '）占用，请先关闭该服务'
        : '端口不可用或被系统保留，请检查其他服务和端口设置');
  setText('httpsText', s.running
    ? 'HTTPS 转发已就绪'
    : s.httpsPortOK
      ? '端口可用，随 CDN 启动'
      : s.httpsPortOwner
        ? '被 ' +
          s.httpsPortOwner.name +
          '（PID ' +
          s.httpsPortOwner.pid +
          '）占用，请先关闭该服务'
        : '端口不可用或被系统保留，请检查其他服务和端口设置');
  $('httpsDot').className = 'dot' + (s.httpsPortOK ? ' good' : '');
  $('portDot').className = 'dot' + (s.portOK ? ' good' : '');
  setText('hostsText', s.hosts.message);
  $('hostsDot').className = 'dot' + (s.hosts.ready ? ' good' : '');
  setText('serviceError', s.cdnError || '');
  for (const source of ['settings', 'hosts', 'queue', 'batch', 'inventory'])
    setText(source + 'ActionError', (s.actionErrors || {})[source] || '');
}

function renderSettings(s) {
  if (!settingsDirty) {
    $('autoStartCDN').checked = !!s.settings.autoStartCDN;
    $('requestRetentionDays').value = s.settings.requestRetentionDays ?? 30;
    $('scanResolveConcurrency').value = s.settings.scanResolveConcurrency || 4;
    $('scanCheckConcurrency').value = s.settings.scanCheckConcurrency || 1;
    $('queuePrefetchCount').value = s.settings.queuePrefetchCount || 3;
    $('storageDir').value = s.settings.storageDir;
    $('logDir').value = s.settings.logDir;
    $('downloadUpstream').value = s.settings.downloadUpstream || 'auto';
    $('upstreamMode').value = s.settings.upstreamMode || 'direct';
    $('socks5Address').value = s.settings.socks5Address || '';
    $('socks5Username').value = s.settings.socks5Username || '';
    $('socks5Password').value = '';
    $('socks5Password').placeholder = s.socks5PasswordSet ? '已保存，留空保持不变' : '需要认证时填写';
    $('maxCacheGiB').value = (s.settings.maxCacheBytes || 0) / 1073741824;
  }
}

function renderControls() {
  const s = lastState;
  const unavailable = !connected || busy || !s;
  for (const b of document.querySelectorAll('button')) b.disabled = unavailable;
  for (const id of [
    'autoStartCDN',
    'storageDir',
    'logDir',
    'maxCacheGiB',
    'requestRetentionDays',
    'downloadUpstream',
    'upstreamMode',
    'socks5Address',
    'socks5Username',
    'socks5Password',
    'scanResolveConcurrency',
    'scanCheckConcurrency',
    'queuePrefetchCount',
    'save',
  ])
    $(id).disabled = unavailable || !!(s.running || s.batch.running || s.queue.running);
  $('socks5Address').disabled ||= $('upstreamMode').value !== 'socks5';
  $('socks5Username').disabled ||= $('upstreamMode').value !== 'socks5';
  $('socks5Password').disabled ||= $('upstreamMode').value !== 'socks5';
  $('socks5Address').required = $('upstreamMode').value === 'socks5';
  $('inventoryScan').disabled = unavailable || !lastInventory || !!lastInventory.scanning;
  setText('settingsAvailability', !connected ? '连接控制台后可修改设置。' : busy
    ? '正在处理操作，请稍候。'
    : s && (s.running || s.batch.running || s.queue.running)
      ? '设置已锁定：请先停止 CDN、队列预缓存和批量任务。'
      : settingsDirty ? '有未保存的更改。保存后用于下一次启动的服务或任务。' : '可以修改设置。保存后用于下一次启动的服务或任务。');
  if (!s) return;
  $('start').disabled = unavailable || !!s.running;
  $('stop').disabled = unavailable || !s.running;
  $('batchStart').disabled = unavailable || !!s.batch.running;
  setText('batchStart', s.batch.running && !s.batch.scanOnly
      ? '正在下载补齐…'
      : s.queue.running
        ? '停止预缓存并下载补齐'
        : '下载补齐');
  $('batchVerify').disabled = unavailable || !!s.batch.running;
  $('batchScan').disabled = unavailable || !!s.batch.running;
  setText('batchScan', s.batch.running && s.batch.scanOnly ? '正在扫描…' : '仅扫描检查');
  $('batchCancel').disabled = unavailable || !s.batch.running;
  $('queueStart').disabled = unavailable || !!s.queue.running;
  $('queueStop').disabled = unavailable || !s.queue.running;
}

function renderQueue(s) {
  const q = s.queue;
  const count = s.settings.queuePrefetchCount || 3;
  setText('queueWindow', '准备队列前 ' + count + ' 个位置中的有效曲目');
  setText('queueStart', q.running
    ? '队列预缓存已开启'
    : s.batch.running && !s.batch.scanOnly
      ? '停止下载补齐并开启预缓存'
      : '开启队列预缓存');
  setText('queuePhase', q.running
    ? q.current
      ? '正在准备歌曲 ' + (q.active || [q.current]).join('、')
      : (q.songs || []).length
        ? '等待队列变化或重试'
        : '等待新的队列同步'
    : '预缓存已停止');
  setText('queueDetail', (q.file || '尚未发现日志') + ' · 本次开启后累计准备成功 ' + q.completed + ' 次');
  setText('queueError', q.logError || q.error || '');
  $('queueSongs').replaceChildren(
    ...(q.songs || []).slice(0, count).map((s) => {
      const li = document.createElement('li');
      li.textContent = s.title || String(s.songId);
      return li;
    }),
  );
}

function renderBatch(s) {
  const b = s.batch;
  const saved = s.lastBatch;
  const hasSaved = saved && saved.updated && !saved.updated.startsWith('0001');
  for (const id of ['total', 'hits', 'downloaded', 'missing'])
    setText(id, hasSaved ? saved[id] || 0 : '—');
  setText('snapshotState', hasSaved
    ? '上次成功' +
      (saved.scanOnly ? '扫描' : '下载补齐') +
      ' · ' +
      new Date(saved.updated).toLocaleString() +
      ' · 已保存，直到下次任务成功才替换'
    : '尚无成功任务结果；扫描或下载补齐成功后保存统计。');
  $('progress').max = b.total || 1;
  $('progress').value = b.checked || 0;
  setText('phase', b.phase || '等待开始');
  setText('current', (b.current || '') +
    (b.total
      ? ' · 当前任务 ' +
        b.checked +
        ' / ' +
        b.total +
        ' · 命中 ' +
        b.hits +
        ' · 扫描缺失或损坏 ' +
        (b.missing || 0) +
        (b.scanOnly
          ? ' · 清单匹配免查询 ' + (b.catalogHits || 0) + ' · 属性复用 ' + (b.reused || 0) +
            ' · 完整校验通过 ' + (b.verified || 0) +
            ' · 损坏 ' + (b.corrupt || 0)
          : '') +
        ' · 下载完成 ' +
        b.downloaded +
        ' · 失败 ' +
        b.failed
      : ''));
  $('failures').hidden = !b.failed;
  $('failureRows').replaceChildren(
    ...(b.failures || []).map((f) => {
      const row = document.createElement('tr');
      for (const text of [f.id + ' · ' + f.name, f.error]) {
        const td = document.createElement('td');
        td.textContent = text;
        row.append(td);
      }
      return row;
    }),
  );
}
// Keep the deadline active through JSON decoding, not just response headers.
async function readState(url) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  try {
    const response = await fetch(url, { signal: controller.signal });
    if (!response.ok) throw Error('控制台连接失败');
    return await response.json();
  } finally {
    clearTimeout(timeout);
  }
}
async function refreshInventory() {
  try {
    const v = await readState('/api/inventory');
    const ready = v.updated && !v.updated.startsWith('0001');
    for (const [id, value] of [
      ['videoCount', v.videos],
      ['cacheBytes', (v.bytes / 1073741824).toFixed(2) + ' GiB'],
    ])
      setText(id, ready ? value : '—');
    const covered = ready && v.coverageKnown && v.totalSongs > 0;
    setText('coverageRate', covered ? (100 * v.coveredSongs / v.totalSongs).toFixed(1) + '%' : '—');
    setText('coverageCount', covered ? v.coveredSongs + ' / ' + v.totalSongs + ' 首曲目已覆盖' : '尚未统计曲目覆盖');
    lastInventory = v;
    setText('inventoryState', v.scanning
      ? '正在扫描，保留上次结果'
      : ready
        ? (v.coverageKnown ? '上次成功刷新 ' : '覆盖率待刷新 · 文件统计 ') + new Date(v.updated).toLocaleString()
        : '尚无成功扫描，请点击「刷新覆盖率」');
    setText('inventoryError', v.error
      ? '本次扫描失败，上次结果保留：' + v.error
      : '');
  } catch (e) {
    lastInventory = null;
    setText('inventoryState', '统计暂不可用');
  } finally {
    renderControls();
  }
}
async function refresh() {
  const revision = settingsRevision;
  try {
    const state = await readState('/api/status');
    // A read started before a successful save may still contain old settings.
    if (revision !== settingsRevision) return;
    connected = true;
    render(state);
    if (uncertainAction)
      notice(
        uncertainAction +
          '\n已刷新当前状态，请核对对应区域；状态快照不能确认原请求是否已经结束。',
      );
  } catch (e) {
    connected = false;
    setText('connection', '控制台连接中断');
    $('connection').className = '';
    renderControls();
    notice(
      (uncertainAction ? uncertainAction + '\n' : '') +
        '无法连接控制台，请检查程序是否仍在运行。',
    );
  }
}
const actionTimeout = 30000;
const hostsActionTimeout = 120000;
async function action(path, body) {
  if (busy) return;
  const origin = document.activeElement;
  busy = true;
  uncertainAction = '';
  renderControls();
  const hosts = path.startsWith('hosts/');
  notice(
    hosts
      ? '正在检查 hosts；需要修改时请在系统提示中允许管理员权限（最多等待 120 秒）…'
      : '正在处理…',
  );
  const controller = new AbortController();
  const timeout = setTimeout(
    () => controller.abort(),
    hosts ? hostsActionTimeout : actionTimeout,
  );
  try {
    const r = await fetch('/api/' + path, {
      method: 'POST',
      headers: {
        'X-StepStash-Token': token,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(body || {}),
      signal: controller.signal,
    });
    // The deadline also covers a response body that never finishes.
    const data = await r.json();
    if (!r.ok) {
      notice(data.error || '操作失败');
      return;
    }
    if (path === 'settings') {
      settingsDirty = false;
      settingsRevision++;
    }
    notice(
      path === 'start'
        ? 'CDN 已启动。若游戏尚未接入，请点击「修改 hosts」。'
        : path === 'settings'
          ? '设置已保存，将用于下一次启动的服务或任务。'
          : path === 'hosts/disable'
            ? '已移除 StepStash 添加的 hosts 映射。其他已有映射保持不变。'
            : '操作已完成。',
    );
  } catch (e) {
    // Losing the response does not cancel or prove failure of a server action.
    uncertainAction =
      (controller.signal.aborted ? '操作等待超时' : '操作响应未能完整读取') +
      '，结果尚未确认。后台可能仍在执行或已完成，请勿重复提交。' +
      (hosts ? '请先检查管理员权限提示和 hosts 状态。' : '');
    notice(uncertainAction + '\n正在核对当前状态…');
  } finally {
    clearTimeout(timeout);
    busy = false;
    await requestRefresh();
    // Disabling a native button can drop focus. Do not steal it if the user moved.
    if (origin && document.activeElement === document.body) {
      const target = origin.disabled ? origin.closest('section[tabindex]') : origin;
      target?.focus({ preventScroll: true });
    }
  }
}
for (const b of document.querySelectorAll('[data-action]'))
  b.addEventListener('click', () => action(b.dataset.action));
for (const event of ['input', 'change'])
  $('settings').addEventListener(event, () => { settingsDirty = true; renderControls(); });
$('settings').addEventListener('submit', (e) => {
  e.preventDefault();
  action('settings', {
    autoStartCDN: $('autoStartCDN').checked,
    requestRetentionDays: Number($('requestRetentionDays').value),
    scanResolveConcurrency: Number($('scanResolveConcurrency').value),
    scanCheckConcurrency: Number($('scanCheckConcurrency').value),
    queuePrefetchCount: Number($('queuePrefetchCount').value),
    downloadUpstream: $('downloadUpstream').value,
    upstreamMode: $('upstreamMode').value,
    socks5Address: $('socks5Address').value,
    socks5Username: $('socks5Username').value,
    ...($('socks5Password').value || !$('socks5Username').value
      ? { socks5Password: $('socks5Password').value } : {}),
    storageDir: $('storageDir').value,
    logDir: $('logDir').value,
    maxCacheBytes: Math.round(Number($('maxCacheGiB').value) * 1073741824),
  });
});
// All refresh triggers share one running batch and one scheduled timer.
const refreshInterval = 5000;
let refreshTimer = null;
let refreshTask = null;
let refreshPending = false;

function scheduleRefresh() {
  clearTimeout(refreshTimer);
  if (!document.hidden)
    refreshTimer = setTimeout(requestRefresh, refreshInterval);
}

function requestRefresh() {
  clearTimeout(refreshTimer);
  if (document.hidden) return;
  refreshPending = true;
  if (refreshTask) return refreshTask;
  refreshTask = (async () => {
    try {
      do {
        refreshPending = false;
        // Finish both reads before starting another batch. A trigger during
        // this batch requests one fresh batch, so action results are not lost.
        await Promise.allSettled([refresh(), refreshInventory()]);
      } while (refreshPending && !document.hidden);
    } finally {
      refreshTask = null;
      scheduleRefresh();
    }
  })();
  return refreshTask;
}

document.addEventListener('visibilitychange', () => {
  if (!document.hidden) requestRefresh();
  else clearTimeout(refreshTimer);
});
renderControls();
requestRefresh();
