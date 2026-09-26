let cachePage = null, cacheOffset = 0, cacheLoading = false, cacheRows = [], cachePending = [];
function cacheLabel(e) {
  return e.songs.length ? e.songs.map(s => (s.title || '未知歌名') + '（ID ' + s.id + '）').join(' / ') : '未知歌名 · 未关联歌曲';
}
function cacheSize(bytes) { return (bytes / 1073741824).toFixed(3) + ' GiB'; }
function cacheChosen() { return cacheRows.filter(r => r.check.checked).map(r => r.entry); }
function updateCacheControls() {
  const unavailable = cacheLoading || busy || !connected;
  for (const id of ['cacheRefresh', 'cacheSearchButton', 'cacheOpen', 'cacheSelect', 'cacheDelete', 'cachePrev', 'cacheNext', 'cacheConfirmDelete', 'cacheCancel']) $(id).disabled = unavailable;
  $('cacheOpen').disabled ||= !cachePage;
  $('cacheSelect').disabled ||= !cacheRows.some(r => r.entry.known && !r.entry.protected);
  $('cacheDelete').disabled ||= !cacheChosen().length;
  $('cachePrev').disabled ||= !cachePage || cacheOffset === 0;
  $('cacheNext').disabled ||= !cachePage || cacheOffset + 50 >= cachePage.total;
  for (const row of cacheRows) {
    row.check.disabled = unavailable || !row.entry.known || row.entry.protected;
    row.locate.disabled = unavailable || !row.entry.known;
  }
  setText('cacheSelected', '已选择 ' + cacheChosen().length + ' 个文件');
}
function cancelCacheConfirmation() {
  cachePending = [];
  $('cacheConfirm').hidden = true;
}
function resetCache() {
  cachePage = null; cacheRows = []; cacheOffset = 0;
  cancelCacheConfirmation();
  $('cacheList').replaceChildren();
  setText('cacheState', '点击刷新明细查看当前存储目录。');
  setText('cacheResult', '');
  updateCacheControls();
}
function renderCache(page) {
  cachePage = page;
  cacheRows = page.entries.map(entry => {
    const row = recentElement('li', 'cache-row');
    const label = recentElement('label', 'cache-choice');
    const check = document.createElement('input'); check.type = 'checkbox';
    const title = cacheLabel(entry);
    check.setAttribute('aria-label', '选择 ' + title);
    const mainTitle = entry.songs.length > 2
      ? entry.songs.slice(0, 2).map(s => (s.title || '未知歌名') + '（ID ' + s.id + '）').join(' / ') + ' 等 ' + entry.songCount + ' 首'
      : title;
    label.append(check, recentElement('strong', '', mainTitle));
    check.addEventListener('change', () => { cancelCacheConfirmation(); updateCacheControls(); });
    const info = recentElement('p', 'muted', cacheSize(entry.bytes) + ' · 最近请求：' + (entry.lastRequest ? new Date(entry.lastRequest).toLocaleString() : '无记录') + ' · ' + entry.state + (entry.protected ? ' · 受保护（正在使用 / 校验 / 待播）' : ''));
    const shared = entry.songCount > 1 ? '共享文件 · 关联 ' + entry.songCount + ' 首歌曲；删除会影响全部关联歌曲。' : '';
    const details = document.createElement('details');
    details.append(recentElement('summary', '', '资源与版本详情'));
    details.append(recentElement('p', '', '资源指纹：' + entry.key));
    details.append(recentElement('p', '', '文件大小：' + entry.bytes.toLocaleString() + ' 字节'));
    for (const song of entry.songs) details.append(recentElement('p', '', (song.title || '未知歌名') + ' · ID ' + song.id + ' · ' + (song.current ? '本地当前引用' : '历史关联（非当前引用）')));
    if (entry.songCount > entry.songs.length) details.append(recentElement('p', '', '仅展示前 ' + entry.songs.length + ' 首；另有 ' + (entry.songCount - entry.songs.length) + ' 首关联歌曲，删除同样受影响。可按歌曲 ID 搜索。'));
    const locate = recentElement('button', '', '定位文件'); locate.type = 'button';
    locate.addEventListener('click', () => cacheAction('open', [entry]));
    row.append(label, info, recentElement('p', 'hint', shared), details, locate);
    return { node: row, entry, check, locate };
  });
  $('cacheList').replaceChildren(...cacheRows.map(r => r.node));
  setText('cacheState', page.total ? '共 ' + page.total + ' 个文件 · 第 ' + (Math.floor(cacheOffset / 50) + 1) + ' 页 · 显示 ' + page.entries.length + ' 项（手动刷新）' : '没有匹配的缓存文件。');
  updateCacheControls();
}
async function refreshCache(reset = false) {
  if (cacheLoading || busy) return;
  if (reset) cacheOffset = 0;
  const revision = settingsRevision, focus = document.activeElement;
  cacheLoading = true; cancelCacheConfirmation(); updateCacheControls();
  setText('cacheState', '正在读取本地缓存明细…');
  try {
    const query = new URLSearchParams({ q: $('cacheSearch').value, sort: $('cacheSort').value, offset: String(cacheOffset) });
    let page;
    // Deletion/eviction can remove the last page. Retry at the last valid
    // offset; if the store shrinks again, fall back to page one (at most 3 reads).
    for (let attempt = 0; attempt < 3; attempt++) {
      query.set('offset', String(cacheOffset));
      page = await readState('/api/cache?' + query, 35000);
      if (revision !== settingsRevision) return;
      if (cacheOffset === 0 || cacheOffset < page.total) break;
      cacheOffset = attempt === 0 ? Math.floor(Math.max(0, page.total - 1) / 50) * 50 : 0;
      if (page.total === 0) break;
    }
    renderCache(page);
  } catch {
    if (revision !== settingsRevision) return;
    cachePage = null; cacheRows = []; $('cacheList').replaceChildren();
    setText('cacheState', '缓存明细读取失败，请重试；未启动服务或联网扫描。');
  } finally {
    cacheLoading = false; updateCacheControls();
    if (document.activeElement === document.body && focus?.isConnected) focus.focus();
  }
}
const cacheResultNames = { deleted: '已删除', protected: '受保护，已跳过', missing: '文件已不存在', unknown: '未知文件，未删除', changed: '文件或歌曲关联已变化，请刷新后重选', unsafe: '目录不安全，未删除', failed: '操作失败，未删除' };
async function cacheAction(action, entries) {
  if (busy || cacheLoading || !cachePage) return;
  const storageID = cachePage.storageID, focus = document.activeElement;
  busy = true; renderControls();
  const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 35000);
  try {
    const response = await fetch('/api/cache/' + action, {
      method: 'POST', headers: { 'X-StepStash-Token': token, 'Content-Type': 'application/json' },
      body: JSON.stringify({ storageID, entries: entries.map(e => ({ key: e.key, stamp: e.stamp })) }), signal: controller.signal,
    });
    const data = await response.json();
    if (!response.ok) throw Error(data.error || '操作失败');
    if (action === 'delete') {
      setText('cacheResult', data.map(r => cacheLabel(entries.find(e => e.key === r.key)) + '：' + (cacheResultNames[r.result] || '结果未知')).join('；'));
      cancelCacheConfirmation();
    } else setText('cacheResult', '已请求系统打开缓存位置。');
  } catch (error) {
    setText('cacheResult', error.name === 'AbortError' ? '操作响应超时，结果未确认。请刷新明细，勿直接重复删除。' : error.message);
    cancelCacheConfirmation();
  } finally {
    clearTimeout(timer); busy = false; renderControls();
    if (action === 'delete') await refreshCache();
    if (document.activeElement === document.body || $('cacheConfirm').contains(document.activeElement)) {
      (action === 'delete' ? $('cacheRefresh') : focus)?.focus();
    }
  }
}
$('cacheSearchForm').addEventListener('submit', event => { event.preventDefault(); refreshCache(true); });
$('cacheRefresh').addEventListener('click', () => refreshCache());
$('cacheOpen').addEventListener('click', () => cacheAction('open', []));
$('cachePrev').addEventListener('click', () => { if (!cacheLoading && !busy) { cacheOffset = Math.max(0, cacheOffset - 50); refreshCache(); } });
$('cacheNext').addEventListener('click', () => { if (!cacheLoading && !busy) { cacheOffset += 50; refreshCache(); } });
$('cacheSelect').addEventListener('click', () => {
  const rows = cacheRows.filter(r => !r.check.disabled), all = rows.every(r => r.check.checked);
  for (const r of rows) r.check.checked = !all;
  cancelCacheConfirmation(); updateCacheControls();
});
$('cacheDelete').addEventListener('click', () => {
  cachePending = cacheChosen();
  if (!cachePending.length) return;
  $('cacheConfirmList').replaceChildren(...cachePending.map(e => recentElement('li', '', cacheLabel(e) + ' · ' + cacheSize(e.bytes) + ' · 影响 ' + e.songCount + ' 首已知关联歌曲')));
  $('cacheConfirm').hidden = false; $('cacheCancel').focus();
});
$('cacheCancel').addEventListener('click', () => { cancelCacheConfirmation(); $('cacheDelete').focus(); });
$('cacheConfirmDelete').addEventListener('click', () => cacheAction('delete', [...cachePending]));
updateCacheControls();
