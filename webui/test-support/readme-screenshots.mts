import { chromium, expect } from '@playwright/test';
import { mockAPI } from '../e2e/api.ts';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, readFile, writeFile } from 'node:fs/promises';

// Run from the repository root with Node.js 22.18+ and installed webui dependencies.
// Renders the production bundle with public song metadata and memory-only activity.
// Caches public song covers in ignored artifacts; never starts the app or game.
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
  const page = await browser.newPage({ baseURL, viewport: { width: 1440, height: 880 }, deviceScaleFactor: 1, locale: 'zh-CN', timezoneId: 'Asia/Shanghai', colorScheme: 'dark', reducedMotion: 'reduce' });
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  const api = await mockAPI(page);
  const at = Date.UTC(2026, 10, 15, 12);
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
  // Public catalog display metadata only; no personal paths, usage or preferences.
  const snapshot = JSON.parse(await readFile('webui/test-support/readme-songs.json', 'utf8'));
  const metadata = snapshot.songs;
  const priority = JSON.parse(await readFile('data/initial-priority/song-priority.json', 'utf8'));
  const seed = JSON.parse(await readFile('data/media-seed/media-seed.json', 'utf8'));
  expect(snapshot.catalogSHA256).toBe(seed.sources.find(source => source.name === 'udon.json').sha256);
  const songs = priority.songs.toSorted((a, b) => b.score - a.score || a.songId - b.songId).slice(0, metadata.length).map((entry, i) => {
    const details = metadata.find(song => song.id === String(entry.songId));
    if (!details) throw Error(`Refresh README song metadata for default-priority song ${entry.songId}`);
    const resource = seed.songMedia.find(song => song.id === entry.songId)?.md5;
    if (!resource) throw Error(`Missing bundled media for song ${entry.songId}`);
    return { ...details, policy: i < 2 ? 'favorite' : 'normal', score: entry.score, hasDemand: false, resource };
  });
  const resources = Object.fromEntries(songs.map((s, i) => {
    const size = seed.media.find(media => media.md5 === s.resource)?.byteSize, refs = [{ id: s.id, title: s.title }];
    if (!size) throw Error(`Missing bundled video size for song ${s.id}`);
    return [s.resource, { key: s.resource, size, duration: s.duration, songs: refs, songCount: 1, local: i < 36 ? { key: s.resource, stamp: 'demo-' + i, bytes: size, songs: refs, songCount: 1, known: true, protected: i === 0, state: '文件存在 · 按 MD5 复用', lastRequest: at - i * 3600000 } : null }];
  }));
  await mkdir('artifacts/readme-refresh/covers', { recursive: true });
  const covers = new Map();
  for (const song of songs) {
    const filename = `artifacts/readme-refresh/covers/${song.id}.jpg`;
    let cover;
    try { cover = await readFile(filename); } catch (error) {
      if (error.code !== 'ENOENT') throw error;
      const response = await fetch(`https://aya.kiva.moe/images/small${song.id}.jpg`, { signal: AbortSignal.timeout(15000) });
      if (response.status === 404) { covers.set(song.id, null); continue; }
      if (!response.ok) throw Error(`Song cover ${song.id}: HTTP ${response.status}`);
      cover = Buffer.from(await response.arrayBuffer());
      await writeFile(filename, cover);
    }
    covers.set(song.id, cover);
  }
  await page.route('**/api/library?*', route => route.fulfill({ json: { storageID: 'fixture', databaseID: 'fixture-db', songs, resources, keys: songs.map(s => s.resource), total: songs.length, fileCount: Object.values(resources).filter(r => r.local).length, totalBytes: Object.values(resources).reduce((sum, r) => sum + (r.local?.bytes || 0), 0), covers: Object.fromEntries(songs.map(s => [s.id, { source: covers.get(s.id) ? 'local' : 'category' }])) } }));
  await page.route('**/api/library/cover?*', route => {
    const cover = covers.get(new URL(route.request().url()).searchParams.get('id'));
    return route.fulfill(cover ? { contentType: 'image/jpeg', body: cover } : { status: 404, body: '暂无封面' });
  });
  await page.route('**/api/inventory', route => route.fulfill({ json: { totalSongs: songs.length, coveredSongs: songs.filter(song => resources[song.resource].local).length, coverageKnown: true, updated: new Date(at).toISOString() } }));
  const readyFirstBodyMS = [.4, .6, .8, 1, 1.2, 1.2, 1.6, 2.4, 3.2, 4.1];
  let readyIndex = 0;
  const history = songs.map((song, i) => {
    const bytes = resources[song.resource].size;
    const sources = i % 4 === 0 ? { downloadBytes: Math.floor(bytes * .6), cacheBytes: bytes - Math.floor(bytes * .6), localBytes: 0 }
      : i % 4 === 1 ? { downloadBytes: 0, cacheBytes: bytes, localBytes: 0 }
      : i % 4 === 2 ? { downloadBytes: 0, cacheBytes: 0, localBytes: bytes }
      : { downloadBytes: bytes, cacheBytes: 0, localBytes: 0 };
    const firstBodyBlocking = sources.downloadBytes > 0;
    const firstBodyMS = firstBodyBlocking ? 180 + (i % 9) * 135 : readyFirstBodyMS[readyIndex++ % readyFirstBodyMS.length];
    const elapsedMS = firstBodyMS + (firstBodyBlocking ? 8500 + i * 123 : 600 + i * 17);
    const received = at - 220000 - i * 420000;
    return { id: songs.length - i, at: received, completedAt: received + elapsedMS, resource: song.resource, songID: song.id, songTitle: song.title, method: 'GET', range: 'bytes=0-', elapsedMS, firstBodyNS: firstBodyMS * 1e6, firstBodyBlocking, bytes, responseBytes: bytes, sources, status: 206, outcome: 'completed' };
  });
  const responseSources = history.reduce((total, record) => {
    for (const key of ['downloadBytes', 'cacheBytes', 'localBytes']) total[key] += record.sources[key];
    return total;
  }, { downloadBytes: 0, cacheBytes: 0, localBytes: 0 });
  const latencyGroup = blocking => {
    const values = history.filter(record => record.firstBodyBlocking === blocking).map(record => record.firstBodyNS / 1e6).sort((a, b) => a - b);
    return { samples: values.length, p50MS: values[Math.ceil(values.length * .5) - 1], p95MS: values[Math.ceil(values.length * .95) - 1] };
  };
  expect(latencyGroup(false)).toMatchObject({ p50MS: 1.2, p95MS: 4.1 });
  await page.route('**/api/requests?*', route => route.fulfill({ json: { storageID: 'fixture', requests: history.slice(0, 1), hasMore: false } }));
  await page.route('**/api/history?*', route => route.fulfill({ json: { storageID: 'fixture', requests: history, hasMore: false, nextCursor: '' } }));
  await page.route('**/api/history/stats*', route => route.fulfill({ json: { from: at - 72 * 3600000, to: at, oldestAt: history.at(-1)?.at ?? null, limit: 10000, requests: history.length, sourceSamples: history.length, sources: responseSources, blocking: latencyGroup(true), ready: latencyGroup(false) } }));
  const blocks = [], demands = [];
  const prepareAheadMS = 500000, open = 16;
  const consumedBlocks = 12;
  // Earlier timed work is ready; only five blocks at the window edge need
  // downloading. Show playback and queue preparation without untimed work.
  const completedBefore = at + 480000;
  for (let row = 0; row < 4; row++) {
    const s = songs[row], count = 40 + row * 8, ids = [];
    for (let i = 0; i < count; i++) {
      const consumed = row === 0 && i < consumedBlocks;
      const needBy = at + (row ? row * 190000 : -60000) + i * 5000;
      const state = consumed ? 'protected' : needBy < completedBefore ? 'available' : 'waiting';
      const id = `${s.resource}/${i}`;
      if (!consumed) ids.push(id);
      // Delivered GET blocks have no remaining use demand. Their local data
      // remains visible during the configured 300-second protection tail.
      blocks.push({ id, resource: s.resource, songID: s.id, title: s.title, block: i, byteOffset: i * 1048576, totalBlocks: count, bytes: 1048576, at: needBy, needBy: consumed ? 0 : needBy, deadline: consumed ? 0 : needBy + 30000, timeBasis: consumed ? 'ended' : 'need-by', protectionStart: consumed ? needBy : 0, protectionUntil: consumed ? needBy + 300000 : 0, protected: consumed, inUse: !consumed, sources: consumed ? 0 : 1, readers: 0, state, windowClosed: false, eligible: !consumed });
    }
    demands.push({ id: `${row + 1}/${s.resource}`, sourceID: row + 1, kind: row ? 'queue' : 'get', songID: s.id, title: s.title, position: row, resource: s.resource, totalBlocks: count, blocks: ids });
  }
  const ready = blocks.filter(b => b.state === 'waiting' && b.needBy <= at + prepareAheadMS)
    .sort((a, b) => a.needBy - b.needBy);
  for (const block of ready.slice(0, open)) block.state = 'downloading';
  const active = blocks.filter(b => b.state === 'downloading');
  expect(active).toHaveLength(5);
  expect(active.filter(b => b.needBy > at + prepareAheadMS)).toEqual([]);
  expect(blocks.filter(b => b.state === 'waiting' && b.needBy <= at + prepareAheadMS)).toEqual([]);
  expect(blocks.filter(b => !b.at)).toEqual([]);
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
  await expect(page.locator('.demand-row')).toHaveCount(4);
  await expect(page.locator('.demand-row').first().getByRole('group')).toHaveAccessibleName(/需要 28 个块，视频共 40 块/);
  await page.getByRole('button', { name: '展开时间轴视图', exact: true }).click();
  await expect(page.locator('.timeline-canvas')).toBeVisible();
  await expect(page.getByRole('switch', { name: '分层显示', exact: true })).not.toBeChecked();
  await expect(page.locator('.timeline-canvas')).toHaveAccessibleName(/208 个块，单层/);
  await page.getByRole('button', { name: '缩小时间轴', exact: true }).click();
  await expect(page.getByRole('button', { name: /^总下载速度：/ })).toHaveText('6.10 MiB/s');
  for (const [label, latency] of [['歌曲列表', 42], ['播放地址', 71], ['视频下载', 64]]) {
    await expect(page.locator('.upstream-card').getByRole('button', { name: `检测${label}，可连接，${latency} ms`, exact: true })).toHaveText(`${label}：${latency} ms`);
  }
  await page.mouse.move(0, 0);
  await page.screenshot({ path: 'docs/images/webui-acceleration.png', animations: 'disabled' });
  await page.goto('/#/library');
  await expect(page.getByRole('button', { name: '切换到列表视图', exact: true })).toBeVisible();
  await expect(page.locator('.library-cover-card')).toHaveCount(songs.length);
  for (const card of await page.locator('.library-cover-card').all()) {
    await expect(card.locator('img.is-ready')).toHaveJSProperty('complete', true);
  }
  // Keep the normal first-page grid filling the viewport, with more songs below.
  await page.setViewportSize({ width: 1440, height: 880 });
  await expect(page.locator('.library-cover-card').first()).toBeInViewport();
  await expect(page.locator('.library-cover-card').nth(17)).toBeInViewport();
  await page.mouse.move(0, 0);
  await page.screenshot({ path: 'artifacts/readme-refresh/webui-library.raw.png', animations: 'disabled' });
  await page.goto('/#/timeline');
  const historyTable = page.getByRole('table', { name: '请求历史列表', exact: true });
  await expect(historyTable.getByRole('row')).toHaveCount(history.length + 1);
  await expect(historyTable.getByRole('row').nth(1)).toContainText(songs[0].title);
  await expect(page.getByRole('region', { name: '响应来源统计' })).toContainText('本地视频');
  await expect(page.getByRole('region', { name: '响应时间', exact: true })).toContainText('P95');
  await expect(page.getByRole('button', { name: '查询历史', exact: true })).toBeVisible();
  await page.mouse.move(0, 0);
  await page.screenshot({ path: 'docs/images/webui-history.png', animations: 'disabled' });
  expect(errors).toEqual([]);
  await writeFile('artifacts/readme-refresh/capture-results.json', JSON.stringify({ viewport: page.viewportSize(), fixture: 'webui/e2e/api.ts', songIDs: songs.map(song => song.id), categoryCoverIDs: songs.filter(song => !covers.get(song.id)).map(song => song.id), catalogRevision: snapshot.catalogRevision, errors, images: 3 }, null, 2));
  console.log('Captured three current WebUI screenshots with real default-priority songs and sample activity; no page errors.');
} finally {
  await browser?.close();
  if (server.exitCode === null) { server.kill(); await once(server, 'exit'); }
}
