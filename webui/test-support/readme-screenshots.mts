import { chromium, expect } from '@playwright/test';
import { mockAPI } from '../e2e/api.ts';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, readFile, writeFile } from 'node:fs/promises';

// Run from the repository root with Node.js 22.18+ and installed webui dependencies.
// Renders the production bundle with memory-only fixtures; never starts the app or game.
const baseURL = 'http://127.0.0.1:18473';
const server = spawn(process.execPath, ['webui/test-support/fixture.mjs'], { windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
let serverError = '';
server.stderr.on('data', data => serverError += data);
let browser;
try {
  await expect.poll(async () => {
    if (server.exitCode !== null) throw Error(serverError || 'Fixture exited');
    try { return (await fetch(baseURL + '/about')).status; } catch { return 0; }
  }).toBe(200);
  browser = await chromium.launch({ channel: 'msedge' });
  const page = await browser.newPage({ baseURL, viewport: { width: 1440, height: 1100 }, deviceScaleFactor: 1, locale: 'zh-CN', timezoneId: 'Asia/Shanghai', colorScheme: 'dark', reducedMotion: 'reduce' });
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  const api = await mockAPI(page);
  const at = Date.UTC(2026, 9, 30, 12);
  await page.clock.setFixedTime(at);
  const observedAt = new Date(at - 60000).toISOString();
  const connectivityResults = [
    { operation: 'catalog', entry: 'api', state: 'available', selected: true, latencyMS: 42, observedAt },
    { operation: 'playback_url', entry: 'cf', state: 'available', latencyMS: 71, observedAt },
    { operation: 'resource', entry: 'example.invalid', state: 'available', latencyMS: 64, observedAt },
  ];
  Object.assign(api.diagnostics.connectivity, { run: 1, revision: 2, phase: 'completed', results: connectivityResults, lastResult: { finished: observedAt, results: connectivityResults } });
  await api.emit('connectivity');
  Object.assign(api.state, { running: true, hosts: { ready: true, managed: true, exitRestorePrepared: true }, activation: { phase: 'received', started: new Date(at - 240000).toISOString(), readyAt: new Date(at - 235000).toISOString(), firstRequest: new Date(at - 220000).toISOString() }, queue: { running: true, desired: true } });
  for (const settings of [api.state.settings, api.state.activeSettings]) Object.assign(settings, { videoStorageMode: 'limited', databasePath: 'D:\\StillWannaDance\\data\\library.sqlite', videoDir: 'D:\\StillWannaDance\\videos' });
  const titles = ['星光舞步', '夏日回响', '晚风节拍', '霓虹派对', '月下旋律', '晴空漫游', '周末律动', '银河来信'];
  const songs = titles.map((title, i) => ({ id: String(1001 + i), title: title + ' · 示例', artist: '示例音乐人', dancer: '示例舞者', group: i % 2 ? '节奏练习' : '周末舞会', policy: i < 2 ? 'favorite' : 'normal', score: 1.28 - i * .13, hasDemand: true, resource: (i + 1).toString(16).padStart(32, '0') }));
  const resources = Object.fromEntries(songs.map((s, i) => {
    const size = (36 + i * 7) * 1048576, refs = [{ id: s.id, title: s.title }];
    return [s.resource, { key: s.resource, size, duration: 183 + i * 11, songs: refs, songCount: 1, local: i < 6 ? { key: s.resource, stamp: 'demo-' + i, bytes: size, songs: refs, songCount: 1, known: true, protected: i === 0, state: '文件存在 · 按 MD5 复用', lastRequest: at - i * 3600000 } : null }];
  }));
  await page.route('**/api/library?*', route => route.fulfill({ json: { storageID: 'fixture', databaseID: 'fixture-db', songs, resources, keys: songs.map(s => s.resource), total: songs.length, fileCount: 6, totalBytes: Object.values(resources).reduce((sum, r) => sum + (r.local?.bytes || 0), 0) } }));
  await page.route('**/api/inventory', route => route.fulfill({ json: { totalSongs: 8, coveredSongs: 6, coverageKnown: true, updated: new Date(at).toISOString() } }));
  await page.route('**/api/requests?*', route => route.fulfill({ json: { storageID: 'fixture', requests: [{ id: 1, at: at - 220000, resource: songs[0].resource, songID: songs[0].id, songTitle: songs[0].title, method: 'GET', range: 'bytes=0-', elapsedMS: 82, bytes: 1048576, status: 206, outcome: 'completed' }], hasMore: false } }));
  const blocks = [], demands = [];
  const prepareAheadMS = 500000, open = 16;
  const consumedBlocks = 12;
  // A steady snapshot: earlier timed work is ready, the window edge is
  // downloading, and untimed work uses only the remaining execution slots.
  const completedBefore = at + 480000;
  for (let row = 0; row < 5; row++) {
    const manual = row === 4;
    const s = songs[manual ? 6 : row], count = manual ? 32 : 40 + row * 8, ids = [];
    for (let i = 0; i < count; i++) {
      const consumed = row === 0 && i < consumedBlocks;
      const needBy = manual ? 0 : at + (row ? row * 190000 : -60000) + i * 5000;
      const state = consumed ? 'protected' : (manual ? i < 8 : needBy < completedBefore) ? 'available' : 'waiting';
      const id = `${s.resource}/${i}`;
      if (!consumed) ids.push(id);
      // Delivered GET blocks have no remaining use demand. Their local data
      // remains visible during the configured 300-second protection tail.
      blocks.push({ id, resource: s.resource, songID: s.id, title: s.title, block: i, byteOffset: i * 1048576, totalBlocks: count, bytes: 1048576, at: needBy, needBy: consumed ? 0 : needBy, deadline: manual || consumed ? 0 : needBy + 30000, timeBasis: consumed ? 'ended' : manual ? 'untimed' : 'need-by', protectionStart: consumed ? needBy : 0, protectionUntil: consumed ? needBy + 300000 : 0, protected: consumed, inUse: !consumed, sources: consumed ? 0 : 1, readers: 0, state, windowClosed: false, opportunistic: manual, eligible: !consumed });
    }
    demands.push({ id: `${row + 1}/${s.resource}`, sourceID: row + 1, kind: manual ? 'batch' : row ? 'queue' : 'get', songID: s.id, title: s.title, position: manual ? 0 : row, resource: s.resource, totalBlocks: count, blocks: ids });
  }
  const ready = blocks.filter(b => b.state === 'waiting' && (b.opportunistic || b.needBy <= at + prepareAheadMS))
    .sort((a, b) => Number(a.opportunistic) - Number(b.opportunistic) || a.needBy - b.needBy);
  for (const block of ready.slice(0, open)) block.state = 'downloading';
  const active = blocks.filter(b => b.state === 'downloading');
  expect(active).toHaveLength(open);
  expect(active.filter(b => !b.opportunistic && b.needBy > at + prepareAheadMS)).toEqual([]);
  expect(blocks.filter(b => b.state === 'waiting' && !b.opportunistic && b.needBy <= at + prepareAheadMS)).toEqual([]);
  expect(demands[0].blocks).toHaveLength(demands[0].totalBlocks - consumedBlocks);
  expect(blocks.filter(b => !b.inUse && b.state === 'protected')).toHaveLength(consumedBlocks);
  const emit = (kind, state) => page.request.post('/__fixture/events', { data: { id: api.fixtureID, kind, state } });
  await emit('blocks', { at, running: true, reset: true, capacity: 4096, used: blocks.filter(b => b.inUse).length, active: active.length, open, prepareAheadMS, downloadGraceMS: 30000, blocks, demands, removed: [], removedDemands: [] });
  await emit('download-speed', { bytesPerSecond: 6400000 });
  await emit('block-cache', { bytes: blocks.filter(b => b.state === 'available' || b.state === 'protected').length * 1048576 });
  await mkdir('docs/images', { recursive: true });
  await mkdir('artifacts/readme-refresh', { recursive: true });
  // Use the existing brand artwork, with the same fixed dark palette as logo.ico.
  const logo = await readFile('internal/branding/logo.svg', 'utf8');
  const darkStyle = '<style media="(prefers-color-scheme: dark)">';
  if (!logo.includes(darkStyle)) throw Error('Brand logo is missing its dark palette');
  await writeFile('docs/images/logo-dark.svg', logo.replace(darkStyle, '<style>'));
  await page.goto('/#/home');
  await expect(page.locator('.demand-row')).toHaveCount(5);
  await expect(page.locator('.demand-row').first().getByRole('group')).toHaveAccessibleName(/需要 28 个块，视频共 40 块/);
  await page.getByRole('button', { name: '展开时间轴视图', exact: true }).click();
  await expect(page.locator('.timeline-canvas')).toBeVisible();
  await page.getByRole('switch', { name: '分层显示', exact: true }).check();
  await expect(page.locator('.timeline-canvas')).toHaveAccessibleName(/240 个块，三层/);
  await page.getByRole('button', { name: '缩小时间轴', exact: true }).click();
  await expect(page.locator('.demand-row').filter({ hasText: '手动下载' })).toBeVisible();
  await expect(page.getByRole('button', { name: /^总下载速度：/ })).toHaveText('6.10 MiB/s');
  for (const [label, latency] of [['歌曲列表', 42], ['播放地址', 71], ['视频下载', 64]]) {
    await expect(page.locator('.upstream-card').getByRole('button', { name: `检测${label}，可连接，${latency} ms`, exact: true })).toHaveText(`${label}：${latency} ms`);
  }
  await page.mouse.move(0, 0);
  await page.screenshot({ path: 'docs/images/webui-acceleration.png', animations: 'disabled' });
  await page.goto('/#/library');
  await expect(page.getByRole('table', { name: '曲库歌曲' }).getByText(songs[7].title, { exact: true })).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 650 });
  await page.screenshot({ path: 'docs/images/webui-library.png', animations: 'disabled' });
  await page.setViewportSize({ width: 1440, height: 650 });
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.goto('/#/settings');
  await page.getByRole('tab', { name: '存储', exact: true }).click();
  await expect(page.getByRole('button', { name: '保存设置', exact: true })).toBeVisible();
  await page.mouse.move(0, 0);
  await page.screenshot({ path: 'docs/images/webui-settings-dark.png', animations: 'disabled', fullPage: true });
  expect(errors).toEqual([]);
  await writeFile('artifacts/readme-refresh/capture-results.json', JSON.stringify({ viewport: page.viewportSize(), fixture: 'webui/e2e/api.ts', errors, images: 3 }, null, 2));
  console.log('Captured three current WebUI screenshots with synthetic data; no page errors.');
} finally {
  await browser?.close();
  if (server.exitCode === null) { server.kill(); await once(server, 'exit'); }
}
