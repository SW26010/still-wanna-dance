const token = document.querySelector('meta[name="stepstash-token"]').content;
const $ = (id) => document.getElementById(id);
let recentLimit = 50, recentStorage = '', recentSnapshot = '', recentRows = [];
let monitorRevision = 0;
let settingsDirty = false,
  settingsRevision = 0,
  exiting = false,
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
  renderActivation(s);
  renderSettings(s);
  renderHealth(s.upstreamMonitor || {}, s.settings || {});
  renderControls();
  renderQueue(s);
  renderBatch(s);
}

function renderHealth(h, settings) {
  const results = h.results || [];
  const states = { available: '可用', unavailable: '不可用', unknown: '待测', stale: '已过期', closed: '已关闭' };
  const reasons = { no_channel: '没有有效候选', no_sample: '等待有效歌曲及资源样本', expired: '观测已过期', timeout: '请求超时', origin_timeout: '源站响应超时（Cloudflare 524）', network_error: '网络请求失败', resolution_unavailable: '无可用资源地址', invalid: '响应校验失败', restricted: '访问受限', upstream_error: '上游错误', http_error: 'HTTP 响应异常' };
  const available = results.filter(r => r.state === 'available').length;
  setText('healthSummary', h.closed ? '上游监测已关闭' : h.checking ? '正在检测所有候选通道…' : !results.length ? '等待首次检测…' : '可用 ' + available + ' / ' + results.length + ' 项（按操作、线路、通道分别测量）');
  setText('healthRoute', '当前检测范围：' + ({ direct: '全部直连 IP', socks5: 'SOCKS5', auto: '全部直连 IP 与 SOCKS5' }[settings.upstreamMode] || '等待网络配置'));
  const dated = v => v && !v.startsWith('0001');
  const date = v => dated(v) ? new Date(v).toLocaleString() : '—';
  setText('healthTime', '最近完成：' + date(h.finished) + ' · 下次检测：' + date(h.nextCheck));
  const groups = [
    ['healthCatalog', 'catalog', 'api'],
    ['healthCatalogKiva', 'catalog', 'kiva'],
    ['healthCatalogWanna', 'catalog', 'wanna'],
    ['healthPlaybackHkg', 'playback_url', 'hkg'],
    ['healthPlaybackCf', 'playback_url', 'cf'],
    ['healthResourceHkg', 'resource', 'hkg'],
    ['healthResourceCf', 'resource', 'cf'],
  ];
  const renderResult = r => {
    const row = document.createElement('li');
    const title = document.createElement('strong');
    title.textContent = (r.mode === 'direct' ? '直连 ' + r.ip : r.mode === 'socks5' ? 'SOCKS5' : '无候选') + ' · ' + (states[r.state] || r.state) + (r.reason ? ' · ' + (reasons[r.reason] || r.reason) : '');
    title.className = r.state === 'available' ? 'good' : '';
    const detail = document.createElement('div');
    detail.className = 'hint';
    detail.textContent = r.entry + (r.operation === 'catalog' ? ' · 响应 time：' + (r.catalogTime || '未获取') : '') + (r.channelID ? ' · 通道 ' + r.channelID : '') +
      (r.estimatedLatencyMS != null ? ' · 首字节 ' + r.estimatedLatencyMS.toFixed(1) + ' ms' : '') +
      (r.estimatedSpeedBPS != null ? ' · 样本吞吐 ' + (r.estimatedSpeedBPS / 1024).toFixed(1) + ' KiB/s' : '') +
      (r.transferDurationMS != null ? ' · 下载 ' + (r.transferDurationMS / 1000).toFixed(2) + ' s / ' + (r.transferredBytes / 1048576).toFixed(2) + ' MiB' : '') +
      (r.sampleSongID ? ' · 歌曲 #' + r.sampleSongID : '') +
      (r.http ? ' · HTTP ' + r.http : '') +
      ' · 观测 ' + date(r.observedAt) + ' · 有效至 ' + date(r.validUntil);
    row.append(title, detail);
    return row;
  };
  for (const [id, operation, route] of groups) {
    const matching = results.filter(r => r.operation === operation && (!route || r.route === route));
    if (operation === 'catalog') {
      const times = [...new Set(matching.map(r => r.catalogTime).filter(Boolean))];
      setText(id + 'Time', '响应 time：' + (times.length ? times.join(' / ') : '未获取'));
    }
    const rows = matching.map(renderResult);
    if (!rows.length) {
      const empty = document.createElement('li');
      empty.className = 'muted';
      empty.textContent = h.closed ? '上游监测已关闭' : h.checking ? '正在检测…' : '暂无检测结果';
      rows.push(empty);
    }
    $(id).replaceChildren(...rows);
  }
  setText('healthCheck', h.checking ? '检测中…' : '立即检测');
}

function activationPending(s) {
  return ['checking', 'starting', 'hosts'].includes(s?.activation?.phase);
}
function renderActivation(s) {
  const a = s.activation || {}, active = activationPending(s);
  const dated = v => v && !v.startsWith('0001');
  const received = dated(a.firstRequest);
  setText('activationService', s.running ? '服务：HTTP 缓存与 HTTPS 转发运行中' : '服务：未运行');
  setText('activationHosts', s.hosts.ready ? 'hosts：接入完成' : 'hosts：' + s.hosts.message);
  setText('activationRequest', received
    ? '视频请求：本次已收到视频请求（不代表播放成功）'
    : '视频请求：' + (a.phase === 'waiting' ? '等待本次视频请求' : '尚未开始或已结束检测'));
  const progress = { checking: '正在检查接入配置…', starting: '正在启动服务并检查端口…',
    hosts: '服务已运行，正在检查 / 修改 hosts；如出现 UAC 提示，请允许管理员权限…',
    failed: '启用未完成：' + a.error, stopped: s.cdnError ? '服务已停止：' + s.cdnError : '本次检测已结束。' };
  setText('activationProgress', progress[a.phase] || (a.phase === 'waiting' ? '启用步骤已完成，请核对下方实时状态。' : '启用向导会复用已有服务和 hosts 配置。'));
  setText('activationNext', active ? '请等待当前操作完成，勿重复提交。' : !s.running
    ? '下一步：启用游戏加速；端口冲突时请在单项管理查看占用信息，关闭冲突程序后重试。'
    : !s.hosts.ready ? '下一步：检查 hosts 冲突或管理员权限后重试。服务已运行，接入尚未完成。'
    : a.phase !== 'waiting' ? '下一步：点击重新检测，建立新的请求观察窗口。'
    : received ? '这里只确认收到请求，无法确认游戏播放。若无法播放，请检查上游连接及日志；已解析的视频传输可在最近请求中查看。'
    : '下一步：在游戏中请求一首歌曲。如一直没有请求，请重新进入世界或重启游戏以刷新 DNS，并确认使用 HTTP 播放地址。');
  setText('activationTime', dated(a.started) ? '本次向导开始：' + new Date(a.started).toLocaleString() +
    (dated(a.readyAt) ? ' · 检测起点：' + new Date(a.readyAt).toLocaleString() : '') +
    (received ? ' · 首个请求到达：' + new Date(a.firstRequest).toLocaleString() : '') : '尚无本次检测；历史统计不用于判断接入。');
  setText('enableAcceleration', active ? '正在启用…' : a.phase === 'failed' ? '重试启用游戏加速'
    : s.running && s.hosts.ready ? '重新检测视频请求' : '启用游戏加速');
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
    ? 'Still Wanna Dance 正在监听'
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
    $('queuePrefetchEnabled').checked = s.settings.queuePrefetchEnabled !== false;
    $('autoStartCDN').checked = !!s.settings.autoStartCDN;
    $('requestRetentionDays').value = s.settings.requestRetentionDays ?? 30;
    $('queuePrefetchCount').value = s.settings.queuePrefetchCount || 3;
    $('storageDir').value = s.settings.storageDir;
    $('logDir').value = s.settings.logDir;
    $('manualLogDir').checked = !!s.settings.manualLogDir;
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
  const unavailable = exiting || !connected || busy || !s || activationPending(s);
  for (const b of document.querySelectorAll('button')) b.disabled = unavailable;
  $('recentMore').disabled ||= $('pauseMonitor').checked;
  $('healthCheck').disabled ||= !!s?.upstreamMonitor?.checking;
  for (const id of [
    'autoStartCDN',
    'queuePrefetchEnabled',
    'storageDir',
    'logDir',
    'manualLogDir',
    'maxCacheGiB',
    'requestRetentionDays',
    'downloadUpstream',
    'upstreamMode',
    'socks5Address',
    'socks5Username',
    'socks5Password',
    'queuePrefetchCount',
    'save',
  ])
    $(id).disabled = unavailable || !!(s.running || s.batch.running);
  $('logDir').disabled ||= !$('manualLogDir').checked;
  $('logDir').required = $('manualLogDir').checked;
  if (!$('manualLogDir').checked && s) $('logDir').value = s.defaultLogDir || s.settings.logDir;
  $('directConnectionHint').hidden = $('upstreamMode').value !== 'direct';
  $('upstreamMode').setAttribute('aria-describedby', $('upstreamMode').value === 'direct' ? 'directConnectionHint' : 'socks5PasswordHint');
  $('socks5Address').disabled ||= $('upstreamMode').value === 'direct';
  $('socks5Username').disabled ||= $('upstreamMode').value === 'direct';
  $('socks5Password').disabled ||= $('upstreamMode').value === 'direct';
  $('socks5Address').required = $('upstreamMode').value !== 'direct';
  $('inventoryScan').disabled = unavailable || !lastInventory || !!lastInventory.scanning;
  if (typeof updateCacheControls === 'function') updateCacheControls();
  setText('settingsAvailability', !connected ? '连接控制台后可修改设置。' : busy
    ? '正在处理操作，请稍候。'
    : s && (s.running || s.batch.running)
      ? '设置已锁定：请先停止 CDN 和批量任务。'
      : settingsDirty ? '有未保存的更改。保存后用于下一次启动的服务或任务。' : '可以修改设置。保存后用于下一次启动的服务或任务。');
  if (!s) return;
  $('start').disabled = unavailable || !!s.running;
  $('stop').disabled = unavailable || !s.running;
  $('batchStart').disabled = unavailable || !!s.batch.running;
  setText('batchStart', s.batch.running && !s.batch.scanOnly
      ? '正在下载补齐…'
      : s.queue.running
        ? '暂停预缓存并下载补齐'
        : '下载补齐');
  $('batchScan').disabled = unavailable || !!s.batch.running;
  setText('batchScan', s.batch.running && s.batch.scanOnly ? '正在扫描…' : '仅扫描检查');
  $('batchCancel').disabled = unavailable || !s.batch.running;
}

function renderQueue(s) {
  const q = s.queue;
  const count = s.settings.queuePrefetchCount || 3;
  setText('queueWindow', '准备队列前 ' + count + ' 个位置中的有效曲目');
  setText('queuePhase', q.running
    ? q.current
      ? '正在准备歌曲 ' + (q.active || [q.current]).join('、')
      : (q.songs || []).length
        ? '等待队列变化或重试'
        : '等待新的队列同步'
    : s.settings.queuePrefetchEnabled === false ? '已在设置中关闭随 CDN 启用'
      : !s.running ? '随本地 CDN 启动'
      : s.batch.running && !s.batch.scanOnly ? '下载补齐期间暂停，结束后自动恢复'
      : '预缓存未就绪，请检查日志目录');
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
  $('batchBudgetHint').hidden = !b.budgetReached;
  setText('current', (b.current || '') +
    (b.total
      ? ' · 当前任务 ' +
        b.checked +
        ' / ' +
        b.total +
        ' · 命中 ' +
        b.hits +
        ' · 缺失 ' +
        (b.missing || 0) +
        (b.scanOnly
          ? ' · 本地文件命中 ' + (b.reused || 0)
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
async function readState(url, timeoutMS = 10000) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMS);
  try {
    const response = await fetch(url, { signal: controller.signal, headers: { 'X-StepStash-Token': token } });
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
    if (exiting || revision !== settingsRevision) return;
    connected = true;
    render(state);
    if (uncertainAction)
      notice(
        uncertainAction +
          '\n已刷新当前状态，请核对对应区域；状态快照不能确认原请求是否已经结束。',
      );
  } catch (e) {
    if (exiting) return;
    connected = false;
    setText('connection', '控制台连接中断');
    setText('activationProgress', '控制台连接中断，接入状态尚未确认；下方为最后一次状态。');
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
  if (busy || exiting) return;
  if (path === 'exit' && !window.confirm('确定退出整个应用？所有服务和后台任务将停止，hosts 映射会保留。需要恢复 hosts 时，请取消并先恢复。')) return;
  const origin = document.activeElement;
  busy = true;
  uncertainAction = '';
  renderControls();
  const hosts = path.startsWith('hosts/') || path === 'activation/enable';
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
    if (path === 'exit') {
      exiting = true;
      clearTimeout(refreshTimer);
      refreshPending = false;
      setText('connection', '退出请求已接受');
      $('connection').className = '';
      setText('serviceBadge', '正在退出应用');
      $('serviceBadge').className = 'pill';
      notice('正在停止所有服务和后台任务，可以关闭此页面。hosts 映射仍保留；再次使用请重新启动应用。');
      renderControls();
      return;
    }
    if (path === 'settings') {
      settingsDirty = false;
      settingsRevision++;
      resetRecent();
      if (typeof resetCache === 'function') resetCache();
    }
    notice(
      path === 'start'
        ? 'CDN 已启动。若游戏尚未接入，请点击「修改 hosts」。'
        : path === 'settings'
          ? '设置已保存，将用于下一次启动的服务或任务。'
          : path === 'hosts/disable'
            ? '已移除 Still Wanna Dance 添加的 hosts 映射。其他已有映射保持不变。'
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
    if (!exiting) await requestRefresh();
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
    queuePrefetchEnabled: $('queuePrefetchEnabled').checked,
    requestRetentionDays: Number($('requestRetentionDays').value),
    queuePrefetchCount: Number($('queuePrefetchCount').value),
    downloadUpstream: $('downloadUpstream').value,
    upstreamMode: $('upstreamMode').value,
    socks5Address: $('socks5Address').value,
    socks5Username: $('socks5Username').value,
    ...($('socks5Password').value || !$('socks5Username').value
      ? { socks5Password: $('socks5Password').value } : {}),
    storageDir: $('storageDir').value,
    manualLogDir: $('manualLogDir').checked,
    logDir: $('manualLogDir').checked ? $('logDir').value : '',
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
  if (!exiting && !document.hidden)
    refreshTimer = setTimeout(requestRefresh, refreshInterval);
}

function requestRefresh() {
  clearTimeout(refreshTimer);
  if (exiting || document.hidden) return;
  refreshPending = true;
  if (refreshTask) return refreshTask;
  refreshTask = (async () => {
    try {
      do {
        refreshPending = false;
        // Finish all reads before starting another batch. A trigger during
        // this batch requests one fresh batch, so action results are not lost.
        const reads = [refresh()];
        if (!$('page-cache').hidden) reads.push(refreshInventory());
        if (!$('page-monitor').hidden && !$('pauseMonitor').checked) reads.push(refreshRecent(), refreshDownloads());
        await Promise.allSettled(reads);
      } while (!exiting && refreshPending && !document.hidden);
    } finally {
      refreshTask = null;
      scheduleRefresh();
    }
  })();
  return refreshTask;
}

document.addEventListener('pagechange', requestRefresh);
$('pauseMonitor').addEventListener('change', () => {
  monitorRevision++;
  setText('monitorRefreshState', $('pauseMonitor').checked
    ? '监控显示已暂停，数据停留在上次刷新；后台任务继续运行。'
    : '每 5 秒刷新；暂停仅冻结监控显示，后台任务继续运行。');
  renderControls();
  if (!$('pauseMonitor').checked) requestRefresh();
});
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) requestRefresh();
  else clearTimeout(refreshTimer);
});
let downloadRows = [];
function renderDownloads(v) {
  setText('downloadSpeed', (v.bytesPerSecond / 1e6).toFixed(3) + ' MB/s');
  setText('downloadState', (v.running ? 'CDN 已开启' : 'CDN 已关闭') + ' · ' +
    (v.tasks.length ? v.tasks.length + ' 个活动任务' : '暂无活动下载任务'));
  const opened = new Set(downloadRows.filter(r => r.details.open).map(r => r.key));
  const focused = downloadRows.find(r => r.summary === document.activeElement)?.key;
  downloadRows = v.tasks.map(task => {
    const key = task.resource + ':' + task.id;
    const item = recentElement('li', 'download-task');
    const main = recentElement('div', 'download-main');
    const songs = task.songs || [], shared = songs.length > 1 || task.moreSongs;
    const title = songs[0]?.title || '未知歌名';
    const song = recentElement('div', 'download-song');
    song.append(recentElement('strong', '', title), recentElement('small', 'recent-label',
      shared ? '等 ' + (task.moreSongs ? '至少 ' : '') + songs.length + ' 首关联曲目 · 播放歌曲未确定' : '资源关联曲目'));
    const stage = ({ cache_check: '检查缓存', upstream_headers: '等待上游',
      download_and_hash: '下载中', publish: '校验并发布', index: '更新索引' })[task.stage] || '阶段未知';
    const route = ({ 'play.udon.dance': 'CF', 'nya.xin.moe': 'HKG' })[task.host] || (task.host ? '未知线路' : '线路待定');
    song.append(recentElement('small', 'recent-label', stage + ' · ' + route));
    const known = task.size > 0;
    const percent = known ? Math.min(100, 100 * task.bytes / task.size) : null;
    main.append(song, recentMetric('进度', known ? percent.toFixed(1) + '%' : '进度未知'),
      recentMetric('上游速度', (task.bytesPerSecond / 1e6).toFixed(3) + ' MB/s'));
    item.append(main);
    if (known) {
      const progress = document.createElement('progress');
      progress.max = task.size;
      progress.value = Math.min(task.bytes, task.size);
      progress.setAttribute('aria-label', title + ' 已读取字节进度');
      item.append(progress);
    }
    item.append(recentElement('small', 'recent-label', (task.bytes / 1e6).toFixed(3) + ' / ' +
      (known ? (task.size / 1e6).toFixed(3) : '未知') + ' MB'));
    const details = recentElement('details', 'download-detail');
    const summary = recentElement('summary', '', '下载详情');
    details.append(summary, recentElement('p', '', requestTitle(task)),
      recentElement('p', '', '实际上游域名：' + (task.host || '尚未确定')),
      recentElement('p', '', '距上次读取 ' + (task.idleMS / 1000).toFixed(0) + ' 秒'),
      recentElement('p', 'recent-resource', '资源：' + task.resource));
    details.open = opened.has(key);
    item.append(details);
    return { key, item, details, summary };
  });
  $('downloadList').replaceChildren(...downloadRows.map(r => r.item));
  if (focused) (downloadRows.find(r => r.key === focused)?.summary || $('downloads')).focus({ preventScroll: true });
}
async function refreshDownloads() {
  const revision = settingsRevision;
  const displayRevision = monitorRevision;
  try {
    const v = await readState('/api/downloads');
    if (revision === settingsRevision && displayRevision === monitorRevision && !$('pauseMonitor').checked) renderDownloads(v);
  } catch {
    if (revision !== settingsRevision || displayRevision !== monitorRevision || $('pauseMonitor').checked) return;
    if ($('downloadList').contains(document.activeElement)) $('downloads').focus({ preventScroll: true });
    downloadRows = [];
    $('downloadList').replaceChildren();
    setText('downloadSpeed', '—');
    setText('downloadState', '活动下载读取失败，将自动重试。');
  }
}
function resetRecent() {
  recentLimit = 50;
  recentStorage = '';
  recentSnapshot = '';
  recentRows = [];
  $('recentList').replaceChildren();
  $('recentMore').hidden = true;
  setText('recentState', '正在读取当前存储目录…');
}

// Process newest first. A non-Range request breaks only its own resource's chain.
function groupRequests(requests) {
  const groups = [], latest = new Map();
  for (const r of requests) {
    const range = r.method === 'GET' && !!r.range;
    let group = latest.get(r.resource);
    if (!range || !group || !group.range || group.first - r.at > 30000) {
      group = { range, first: r.at, last: r.at, end: r.at, bytes: 0, requests: [] };
      groups.push(group);
    }
    group.first = r.at;
    // GET timestamps can be reset after URL resolution, while elapsedMS
    // includes resolution. This inferred end is only a span estimate.
    group.end = Math.max(group.end, r.at + r.elapsedMS);
    group.bytes += r.bytes;
    group.requests.push(r);
    latest.set(r.resource, group);
  }
  return groups;
}
function requestOutcome(r) {
  if (r.outcome === 'canceled') return '客户端取消 / 请求中断';
  if (r.outcome === 'aborted') return '传输中止（原因未确定）';
  if (r.outcome === 'incomplete') return '传输不完整';
  if (r.outcome === 'failed') return r.status >= 400 ? '请求失败' : '写入失败 / 连接中断';
  if (r.outcome === 'completed') return r.status === 200 || r.status === 206
    ? (r.method === 'HEAD' ? '响应头处理完成' : '请求处理完成') : 'HTTP 响应结束';
  return '结果未知';
}
function requestCache(r) {
  return ({ HIT: '缓存命中', MISS: '回源', WAIT: '等待共享下载', UNKNOWN: '缓存状态未知' })[r.cache] || '缓存状态未知';
}
function requestTitle(r) {
  const songs = r.songs || [];
  if (!songs.length) return '未知歌名 · 资源 ' + r.resource;
  return (songs.length > 1 || r.moreSongs ? '关联曲目（播放歌曲未确定）：' : '关联曲目：') +
    songs.map(s => (s.title || '未知歌名') + '（ID ' + s.id + '）').join(' / ') + (r.moreSongs ? ' / …更多关联曲目' : '');
}
function recentElement(tag, className, text) {
  const element = document.createElement(tag);
  element.className = className;
  if (text != null) element.textContent = text;
  return element;
}
function recentMetric(label, value) {
  const cell = recentElement('span', 'recent-metric');
  cell.append(recentElement('small', 'recent-label', label), recentElement('span', 'recent-value', value));
  return cell;
}
function recentBadges(requests, label, className) {
  const counts = new Map();
  for (const r of requests) counts.set(label(r), (counts.get(label(r)) || 0) + 1);
  return [...counts].map(([name, n]) => recentElement('span', 'recent-badge ' + className, name + ' ×' + n));
}
function recentSummary(g) {
  const summary = recentElement('summary', 'recent-summary');
  const first = g.requests[0], songs = first.songs || [];
  const song = recentElement('span', 'recent-song');
  const title = songs.length ? songs.slice(0, 2).map(s => s.title || '未知歌名（ID ' + s.id + '）').join(' / ') : '未知歌名';
  song.append(recentElement('strong', 'recent-song-title', title));
  if (songs.length > 2 || first.moreSongs)
    song.append(recentElement('small', 'recent-label', '等 ' + (first.moreSongs ? '至少 ' : '') + songs.length + ' 首关联曲目 · 全部候选见明细'));
  song.append(recentElement('small', 'recent-label', songs.length > 1 || first.moreSongs
    ? '共享资源 · 实际播放歌曲未确定' : songs.length ? '资源关联曲目' : '资源标识见展开明细'));
  const meta = recentElement('span', 'recent-meta');
  meta.append(recentElement('span', '', new Date(g.last).toLocaleString()),
    recentElement('span', 'recent-badge', '请求 ×' + g.requests.length), recentElement('span', 'recent-expand', '明细'));
  song.append(meta);
  const status = recentElement('span', 'recent-status');
  status.append(recentElement('small', 'recent-label', '缓存 / 结果'),
    ...recentBadges(g.requests, requestCache, 'recent-cache'), ...recentBadges(g.requests, requestOutcome, 'recent-outcome'));
  const ms = g.end - g.first, grouped = g.requests.length > 1;
  summary.append(song, status,
    recentMetric('实际传输', (g.bytes / 1e6).toFixed(3) + ' MB'),
    recentMetric(grouped ? '估算跨度' : '耗时', (ms / 1000).toFixed(3) + ' 秒'),
    recentMetric(grouped ? '估算平均速度' : '平均速度', ms > 0 ? (g.bytes / ms / 1000).toFixed(3) + ' MB/s' : '—'));
  return summary;
}
function transferText(bytes, ms) {
  return (bytes / 1e6).toFixed(3) + ' MB · ' + (ms / 1000).toFixed(3) + ' 秒 · ' +
    (ms > 0 ? (bytes / ms / 1000).toFixed(3) + ' MB/s' : '速度 —');
}
function renderRecent(v) {
  if (recentStorage && recentStorage !== v.storageID) resetRecent();
  recentStorage = v.storageID;
  const signature = JSON.stringify(v.requests);
  if (signature !== recentSnapshot) {
    const opened = new Set(recentRows.filter(row => row.node.open).flatMap(row => row.ids));
    const focused = recentRows.find(row => row.summary === document.activeElement);
    recentRows = groupRequests(v.requests).map(g => {
      const node = document.createElement('details');
      const summary = recentSummary(g);
      const first = g.requests[0];
      node.append(summary);
      const detail = recentElement('div', 'recent-detail');
      detail.append(recentElement('p', 'recent-associations', requestTitle(first)),
        recentElement('p', 'recent-resource', '资源标识：' + first.resource));
      const list = document.createElement('ul');
      for (const r of g.requests) {
        const item = document.createElement('li');
        item.textContent = new Date(r.at).toLocaleString() + ' · ' + r.method + ' ' + (r.range || '完整请求') +
          ' · HTTP ' + (r.status || '未返回') + ' · ' + requestCache(r) + ' · ' + requestOutcome(r) +
          ' · ' + transferText(r.bytes, r.elapsedMS) + ' · 资源 ' + r.resource;
        list.append(item);
      }
      detail.append(list);
      node.append(detail);
      const ids = g.requests.map(r => r.resource + ':' + r.at + ':' + r.id);
      node.open = ids.some(id => opened.has(id));
      return { node, summary, ids };
    });
    $('recentList').replaceChildren(...recentRows.map(row => row.node));
    if (focused) (recentRows.find(row => row.ids.some(id => focused.ids.includes(id)))?.summary || $('recent')).focus({ preventScroll: true });
    recentSnapshot = signature;
  }
  $('recentMore').hidden = !v.hasMore || recentLimit >= 500;
  if ($('recentMore').hidden && document.activeElement === $('recentMore')) $('recent').focus({ preventScroll: true });
  setText('recentState', (v.requests.length ? '已显示 ' + v.requests.length + ' 条 HTTP 请求' : '当前窗口暂无 HTTP 请求记录') +
    ' · 最近 ' + recentLimit + ' 条 HTTP 请求窗口' + (v.hasMore ? ' · 窗口外还有 HTTP 请求，分组可能不完整' : '') +
    (recentLimit >= 500 && v.hasMore ? '（已达 500 条上限）' : ''));
}
async function refreshRecent() {
  const revision = settingsRevision;
  const displayRevision = monitorRevision;
  try {
    const v = await readState('/api/requests?limit=' + recentLimit);
    if (revision !== settingsRevision || displayRevision !== monitorRevision || $('pauseMonitor').checked) return;
    renderRecent(v);
  } catch {
    if (revision !== settingsRevision || displayRevision !== monitorRevision || $('pauseMonitor').checked) return;
    // Do not present stale data from an unknown storage directory as current.
    if ($('recentList').contains(document.activeElement) || document.activeElement === $('recentMore')) $('recent').focus({ preventScroll: true });
    recentSnapshot = '';
    recentRows = [];
    $('recentList').replaceChildren();
    $('recentMore').hidden = true;
    setText('recentState', '最近请求读取失败，将自动重试。');
  }
}
$('recentMore').addEventListener('click', () => {
  if ($('pauseMonitor').checked) return;
  recentLimit = Math.min(500, recentLimit + 50);
  requestRefresh();
});
renderControls();
requestRefresh();
