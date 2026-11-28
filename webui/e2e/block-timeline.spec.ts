import { test, expect, type Page } from '@playwright/test';
import { writeFile } from 'node:fs/promises';
import { mockAPI } from './api';

async function setPageHidden(page: Page, hidden: boolean) {
  await page.evaluate(value => { Object.defineProperty(document, 'hidden', { configurable: true, value }); document.dispatchEvent(new Event('visibilitychange')); }, hidden);
}
import { navigate } from './navigation';
import type { BlockView, BlockEvent, BlockSource, BlockDemand, BlockDetail } from '../src/block-timeline-model';
import { applyBlockEvent } from '../src/block-timeline-model';
import { timeScale, timelineLayout, timelineRange, untimedLayout } from '../src/block-timeline-canvas';
const singleY = timelineLayout(false, true, false, 16).timedY + 8;
const laneCenter = (row: number, size: number) => {
  const layout = timelineLayout(true, true, false, size);
  return layout.timedY + row * layout.lanePitch + size / 2;
};

function block(index: number, at: number, patch: Partial<BlockView> = {}): BlockView {
  return { id: `resource/${index}`, resource: 'fixture-resource', songID: '42', block: index, byteOffset: index * 1048576, totalBlocks: 0, bytes: 1048576, title: '星河 · 测试歌曲', at, timeBasis: at ? 'need-by' : 'untimed', needBy: at, deadline: at ? at + 30000 : 0, protectionStart: 0, protectionUntil: 0, protected: false, inUse: true, sources: 2, readers: 0, state: 'waiting', windowClosed: false, opportunistic: false, eligible: true, ...patch };
}
function demand(id: number, blocks: BlockView[], patch: Partial<BlockDemand> = {}): BlockDemand {
  return { id: `${id}/fixture-resource`, sourceID: id, kind: 'get', songID: '42', title: '星河 · 测试歌曲', position: 0, resource: 'fixture-resource', totalBlocks: Math.max(0, ...blocks.map(b => b.block + 1)), blocks: blocks.map(b => b.id), gameSongID: !patch.kind || patch.kind === 'get' ? '42' : undefined, ...patch };
}
async function setup(page: Page, count = 48) {
  const api = await mockAPI(page), at = Date.now();
  api.state.running = true;
  const blocks = Array.from({ length: count }, (_, i) => block(i, at - 90000 + i * (count > 100 ? 0.2 : 14000), {
    state: i < 4 ? 'protected' : i < 15 ? 'available' : i < 21 ? 'downloading' : 'waiting',
    inUse: i >= 4, sources: i < 4 ? 0 : 2, readers: i === 17 ? 1 : 0,
    timeBasis: i < 4 ? 'last-need-by' : 'need-by', protected: i < 4, protectionUntil: i < 4 ? at + 45000 : 0,
  }));
  const initial: BlockEvent = { at, running: true, reset: true, capacity: 4096, used: count - 4, active: 6, open: 16, prepareAheadMS: 500000, downloadGraceMS: 30000, blocks, removed: [], demands: [], removedDemands: [] };
  const emit = async (state: Partial<BlockEvent>) => page.request.post('/__fixture/events', { data: { id: api.fixtureID, kind: 'blocks', state: { ...initial, reset: false, blocks: [], ...state } } });
  await emit(initial);
  const source: BlockSource = { id: 1, kind: 'get', songID: '42', title: '星河', position: 0, needBy: at, expiresAt: 0, windowClosed: false };
  const detail = async (b: BlockView, sources: BlockSource[] = [source, { ...source, id: 2, kind: 'queue', position: 3 }], route: Pick<BlockDetail, 'upstreamIP' | 'viaProxy'> = {}) => page.request.post('/__fixture/events', { data: { id: api.fixtureID, kind: 'block-detail:' + b.id, state: { block: b, sources, ...route } } });
  for (const b of blocks.slice(0, 48)) await detail(b, b.inUse ? undefined : []);
  const streams = async () => {
    const active = await (await page.request.get('/__fixture/streams?id=' + api.fixtureID)).json() as Record<string, number>;
    return Object.fromEntries(Object.entries(active).filter(([kind]) => kind === 'blocks' || kind.startsWith('block-detail:')));
  };
  const totalStreams = async () => Object.values(await streams()).reduce((sum, n) => sum + n, 0);
  await page.goto('/#/home');
  const panel = page.locator('#block-demands'), canvas = panel.locator('.timeline-canvas'), viewport = panel.locator('.block-timeline');
  const axis = () => viewport.evaluate((el: HTMLElement) => ({
    center: Number(el.dataset.center), span: Number(el.dataset.span),
    width: el.querySelector('.timeline-canvas')!.clientWidth,
  }));
  const xAt = async (time: number) => timeScale(await axis())(time);
  const open = async () => { await panel.getByRole('button', { name: '展开时间轴视图' }).click(); await expect(panel.getByText('状态展示：更新中', { exact: true })).toBeVisible(); await expect(canvas).toBeVisible(); await canvas.scrollIntoViewIfNeeded(); await expect(canvas).toBeInViewport(); };
  const hidePanel = async () => {
    await page.evaluate(() => window.scrollTo(0, 0));
    const top = await panel.evaluate(el => el.getBoundingClientRect().top);
    await page.setViewportSize({ width: page.viewportSize()!.width, height: Math.floor(top) - 1 });
    await expect(panel).not.toBeInViewport();
  };
  return { ...api, at, blocks, emit, detail, streams, totalStreams, panel, canvas, viewport, axis, xAt, open, hidePanel };
}

test('block details use engine byte offsets for custom sizes and tails', async ({ page }) => {
  const f = await setup(page, 1);
  const tail = block(2, f.at, { byteOffset: 1048576, bytes: 123, totalBlocks: 3 }); // 512 KiB blocks.
  await f.emit({ reset: true, blocks: [tail], used: 1 });
  await f.detail(tail);
  await f.open(); await f.canvas.focus(); await f.canvas.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup.getByText('需求截止', { exact: true })).toBeVisible();
  await expect(f.panel.locator('.timeline-window-key.grace')).toHaveText('需求截止');
  await expect(popup).toContainText('1,048,576 – 1,048,698');
  await expect(popup).toContainText('块 3/3');
  await expect(popup.getByText('fixture-resource',{exact:true})).toHaveCSS('font-family',/Consolas|monospace/);
});

test('block details follow the current upstream IP', async ({ page }) => {
  const f = await setup(page, 1);
  const b = block(0, f.at, { state: 'downloading' });
  await f.emit({ reset: true, blocks: [b], used: 1 });
  await f.detail(b, [], { upstreamIP: '2001:db8::1' });
  await f.open(); await f.canvas.focus(); await f.canvas.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  const value = popup.locator('dt').filter({ hasText: '当前上游 IP' }).locator('+ dd');
  await expect(value).toHaveText('2001:db8::1');
  await f.detail(b, []);
  await expect(value).toHaveText('尚未确认');
  await f.detail(b, [], { upstreamIP: '192.0.2.1' });
  await expect(value).toHaveText('192.0.2.1');
  await f.detail(b, [], { viaProxy: true });
  await expect(value).toHaveText('由代理选路');
  await f.detail({ ...b, state: 'available' }, []);
  await expect(value).toHaveCount(0);
});

test('rolling queue times advance and reorder without receiving another snapshot', async ({ page }) => {
  await page.clock.install();
  const f = await setup(page, 2);
  const base = f.at + 3000;
  const head = block(0, base, { rollingBase: base, deadline: 0, totalBlocks: 2 });
  const fixed = block(1, f.at + 6500, { totalBlocks: 2 });
  await f.detail(head, [{ id: 1, kind: 'queue', songID: '42', title: '星河', position: 1, needBy: base, rollingBase: base, expiresAt: 0, windowClosed: false }]);
  await f.emit({ reset: true, blocks: [head, fixed], used: 2 });
  await f.open();
  await f.canvas.focus();
  await f.canvas.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup).toContainText('块 1/2');
  const needBy = popup.locator('dd').nth(1), deadline = popup.locator('dd').nth(2), original = await needBy.innerText();
  await expect(deadline).toHaveText('—');
  await page.clock.runFor(8500);
  await expect(needBy).not.toHaveText(original);
  await expect(deadline).toHaveText('—');
  await f.canvas.press('Home');
  await expect(popup).toContainText('块 2/2');
});

test('reduced motion samples rolling details with the canvas on redraw', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.clock.install();
  await page.addInitScript(() => {
    const fillText = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function (label, ...args) {
      if (label.startsWith('现在 ')) this.canvas.dataset.nowTime = label.slice(3);
      fillText.call(this, label, ...args);
    };
  });
  const f = await setup(page, 1);
  const head = block(0, f.at, { rollingBase: f.at, deadline: 0, sources: 1, totalBlocks: 1 });
  await f.detail(head, [{ id: 1, kind: 'queue', songID: '42', title: '星河', position: 1, needBy: f.at, rollingBase: f.at, expiresAt: 0, windowClosed: false }]);
  await f.emit({ reset: true, blocks: [head], used: 1 });
  await f.open();
  await f.canvas.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  const needBy = popup.locator('dd').nth(1), source = popup.locator('.timeline-sources');
  const checkSample = async () => {
    const stamp = await needBy.innerText();
    await expect(f.canvas).toHaveAttribute('data-now-time', stamp.split(' ').pop()!.padStart(8, '0'));
    await expect(source).toContainText(stamp);
    await expect(f.panel.locator('.timeline-selection-status')).toContainText(stamp);
    return stamp;
  };
  const original = await checkSample();
  await page.clock.runFor(3000);
  expect(await needBy.innerText()).toBe(original);
  await checkSample();
  await f.canvas.press('=');
  await expect(needBy).not.toHaveText(original);
  await checkSample();
});

test('block details keep the fixed source deadline while need-by rolls', async ({ page }) => {
  await page.clock.install();
  const f = await setup(page, 1);
  const fixed = f.at + 60000, deadline = fixed + 30000;
  const shared = block(0, f.at, { rollingBase: f.at, fixedNeedBy: fixed, deadline, totalBlocks: 1 });
  const common = { songID: '42', title: '星河', expiresAt: 0, windowClosed: false };
  await f.detail(shared, [
    { ...common, id: 1, kind: 'queue', position: 1, needBy: f.at, rollingBase: f.at },
    { ...common, id: 2, kind: 'get', position: 0, needBy: fixed },
  ]);
  await f.emit({ reset: true, blocks: [shared], used: 1 });
  await f.open();
  await f.canvas.focus();
  await f.canvas.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  const needBy = popup.locator('dd').nth(1), cutoff = popup.locator('dd').nth(2);
  const expected = await page.evaluate(at => new Date(at).toLocaleString('zh-CN', { hour12: false }), deadline);
  await expect(cutoff).toHaveText(expected);
  const original = await needBy.innerText();
  await page.clock.runFor(3000);
  await expect(needBy).not.toHaveText(original);
  await expect(cutoff).toHaveText(expected);
});

test('timeline subscribes only while visible and restores a fresh snapshot', async ({ page }) => {
  const f = await setup(page);
  await expect.poll(f.totalStreams).toBe(1);
  await f.open(); await expect.poll(f.totalStreams).toBe(1);
  await f.canvas.focus(); await f.canvas.press('Home');
  await expect(page.getByRole('group', { name: '块需求详情' })).toBeVisible();
  await expect.poll(f.totalStreams).toBe(2);
  await page.getByRole('button', { name: '关闭块详情' }).click();
  await setPageHidden(page, true);
  await expect.poll(f.totalStreams).toBe(0);
  const frozen = await f.canvas.evaluate(c => (c as HTMLCanvasElement).toDataURL());
  await f.emit({ reset: true, blocks: [block(100, f.at)], used: 1 });
  expect(await f.canvas.evaluate(c => (c as HTMLCanvasElement).toDataURL())).toBe(frozen);
  await setPageHidden(page, false);
  await expect(f.canvas).toHaveAccessibleName(/1 个块/);
  await expect.poll(f.totalStreams).toBe(1);
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange')); });
  await expect.poll(f.totalStreams).toBe(0);
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange')); });
  await expect.poll(f.totalStreams).toBe(1);
  await navigate(page, '曲库'); await expect.poll(f.totalStreams).toBe(0);
  await navigate(page, '加速'); await f.panel.scrollIntoViewIfNeeded(); await expect.poll(f.totalStreams).toBe(1);
  await f.hidePanel(); await expect.poll(f.totalStreams).toBe(0);
  await f.panel.scrollIntoViewIfNeeded(); await expect.poll(f.totalStreams).toBe(1);
  await f.panel.getByRole('button', { name: '收起时间轴视图' }).click(); await expect.poll(f.totalStreams).toBe(1);
});

for (const view of ['timeline', 'demand'] as const) test(`pinned ${view} details survive offscreen protection updates but close when the block leaves`, async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page, 4);
  const detailRequests: string[] = [];
  page.on('request', request => {
    const url = new URL(request.url());
    if (url.pathname === '/api/block-demands/events' && url.searchParams.has('block')) detailRequests.push(url.search);
  });
  const blocks = f.blocks.map((b, i) => ({ ...b, at: f.at + i * 10000, needBy: f.at + i * 10000, totalBlocks: 4, state: 'downloading' as const, inUse: true, sources: 1 }));
  const row = demand(1, blocks);
  await f.emit({ reset: true, blocks, demands: [row], used: 4 });
  await f.detail(blocks[0]); await f.open();
  const target = view === 'timeline' ? f.canvas : f.panel.locator('.demand-block-canvas');
  await target.focus(); await target.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup).toContainText('块 1/4');
  await expect.poll(f.totalStreams).toBe(2);

  // A real visibility transition reproduces the intermittent screenshot
  // transition without relying on screenshot timing or synthetic observers.
  await f.hidePanel();
  await expect.poll(f.totalStreams).toBe(0);
  await expect(popup).toHaveCount(0);
  const protectedBlocks = blocks.map(b => ({ ...b, state: 'protected' as const, inUse: false, sources: 0, needBy: 0, timeBasis: 'last-need-by' as const, protected: true, protectionUntil: f.at + 60000 }));
  await f.detail(protectedBlocks[0], []);
  await f.emit({ blocks: protectedBlocks, removedDemands: [row.id], used: 0, active: 0 });
  await page.setViewportSize({width:1280,height:900}); await f.canvas.scrollIntoViewIfNeeded();
  await expect(f.panel.locator('.demand-row')).toHaveCount(0);
  await expect(popup).toContainText('已无使用需求，本地数据仍在延续保护期');
  await expect(popup).toContainText('块 1/4');
  await expect.poll(f.totalStreams).toBe(2);
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  await expect(f.canvas).toBeFocused(); // The original demand row may be gone.
  await expect.poll(f.totalStreams).toBe(1);

  await f.canvas.press('Home'); await expect(popup).toContainText('块 1/4');
  await f.hidePanel();
  await expect.poll(f.totalStreams).toBe(0);
  await f.emit({ removed: [blocks[0].id] });
  const beforeResume = detailRequests.length;
  await page.setViewportSize({width:1280,height:900}); await f.canvas.scrollIntoViewIfNeeded();
  await expect(f.canvas).toHaveAccessibleName(/3 个块/);
  await expect(popup).toHaveCount(0);
  await expect(f.panel.locator('.timeline-selection-status')).toBeEmpty();
  await expect.poll(f.streams).toEqual({ blocks: 1, [`block-detail:${blocks[0].id}`]: 0 });
  expect(detailRequests).toHaveLength(beforeResume);
});

test('hidden page preserves pinned details until an explicit close', async ({ page }) => {
  const f = await setup(page); await f.open();
  await f.canvas.focus(); await f.canvas.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup).toContainText('块 1/?');
  await expect(popup).toContainText('已无使用需求，本地数据仍在延续保护期');
  await expect.poll(f.totalStreams).toBe(2);
  await setPageHidden(page, true);
  await expect.poll(f.totalStreams).toBe(0); await expect(popup).toHaveCount(0);
  await setPageHidden(page, false);
  await expect(popup).toContainText('块 1/?'); await expect.poll(f.totalStreams).toBe(2);
  // Pinned details can cover nearby controls; keyboard access keeps the selection open.
  const layered = f.panel.getByRole('button', { name: '分层显示' });
  await layered.focus(); await layered.press('Space'); await expect(layered).toHaveAttribute('aria-pressed', 'true');
  await expect(popup).toContainText('块 1/?');
  await popup.getByRole('button', { name: '关闭块详情' }).focus();
  await page.keyboard.press('Escape');
  await expect(popup).toHaveCount(0); await f.canvas.scrollIntoViewIfNeeded(); await expect.poll(f.totalStreams).toBe(1);
});

test('removed demand rows restore focus to their region without stealing outside focus', async ({ page }) => {
  const f = await setup(page);
  const rows = [demand(1, f.blocks), demand(2, f.blocks, { kind: 'queue', position: 1 })];
  await f.emit({ demands: rows }); await f.open();
  const region = f.panel.getByRole('region', { name: '视频需求视图' });
  await region.locator('canvas').first().focus();
  await f.emit({ removedDemands: [rows[0].id] });
  await expect(region).toBeFocused();
  await page.keyboard.press('Tab'); await expect(region.locator('canvas')).toBeFocused();
  await f.emit({ removedDemands: [rows[1].id] });
  await expect(region).toBeFocused(); await expect(region.locator('canvas')).toHaveCount(0);
  await f.emit({ demands: [rows[0]] });
  await region.locator('canvas').focus();
  const outside = f.panel.getByRole('button', { name: '分层显示' });
  await outside.focus(); await f.emit({ removedDemands: [rows[0].id] });
  await expect(region.locator('canvas')).toHaveCount(0); await expect(outside).toBeFocused();
});

test('demand titles prefix the song ID and handle missing metadata', async ({ page }) => {
  const f = await setup(page, 1);
  await f.emit({ demands: [
    demand(1, f.blocks, { kind: 'formal', playback: { zeroAt: f.at, endAt: f.at + 60000 } }),
    demand(2, f.blocks, { songID: '', title: '直链视频', gameSongID: undefined }),
    demand(3, f.blocks, { songID: '', title: '', gameSongID: undefined }),
    demand(4, f.blocks, { kind: 'queue', position: 2, songID: '84' }),
    demand(5, f.blocks, { kind: 'batch', title: '' }),
  ] });
  const titles = f.panel.locator('.demand-video-title');
  await expect(titles).toHaveText(['42 · 星河 · 测试歌曲', '直链视频', '视频 fixture-reso', '84 · 星河 · 测试歌曲', '歌曲 42']);
  await expect(titles.nth(3)).toHaveAttribute('title', '84 · 星河 · 测试歌曲');
  await expect(f.panel.locator('.demand-block-canvas').nth(3)).toHaveAccessibleName(/^队列 #2，84 · 星河 · 测试歌曲，/);
});

test('demand rows hide handoffs and only suppress the same song at the queue head while GET is active', async ({ page }, info) => {
  const f = await setup(page);
  const queue = demand(1, f.blocks, { kind: 'queue', position: 1 });
  const later = demand(2, f.blocks, { kind: 'queue', position: 2 });
  const handoff = demand(3, f.blocks, { kind: 'handoff' });
  const get = demand(4, [f.blocks[0]]), secondGet = demand(5, [f.blocks[1]]);
  const manual = demand(6, f.blocks, { kind: 'batch' });
  await f.emit({ demands: [queue, later, handoff, manual] });
  const headings = f.panel.locator('.demand-row-heading');
  await expect(headings).toHaveText([/队列 #1/, /队列 #2/, /手动下载/]);
  await f.open();
  await f.panel.locator('[data-demand-id="1/fixture-resource"] canvas').focus();
  await f.emit({ demands: [get, secondGet] });
  await expect(headings).toHaveText([/读取 #4/, /读取 #5/, /队列 #2/, /手动下载/]);
  await expect(f.panel.getByRole('region', { name: '视频需求视图' })).toBeFocused();
  await expect(f.panel.locator('.demand-view-heading')).toHaveText('需求视图4 项需求');
  const items = f.panel.getByRole('listitem');
  await expect(items).toHaveCount(4);
  await expect(items.last()).toHaveAttribute('aria-posinset', '4');
  await expect(items.last()).toHaveAttribute('aria-setsize', '4');
  await f.panel.screenshot({ path: info.outputPath('filtered-demand-rows.png') });
  await f.emit({ removedDemands: [get.id] });
  await expect(headings).toHaveText([/读取 #5/, /队列 #2/, /手动下载/]);
  await f.emit({ removedDemands: [secondGet.id] });
  await expect(headings).toHaveText([/队列 #1/, /队列 #2/, /手动下载/]);
  // Shared resources alone do not establish song identity; unknown songs stay separate too.
  await f.emit({ demands: [{ ...get, songID: '84', gameSongID: '84' }] });
  await expect(headings).toHaveText([/读取 #4/, /队列 #1/, /队列 #2/, /手动下载/]);
  await f.emit({ demands: [{ ...get, gameSongID: undefined }] });
  await expect(headings).toHaveText([/读取 #4/, /队列 #1/, /队列 #2/, /手动下载/]);
  await f.emit({ reset: true, blocks: f.blocks, demands: [handoff] });
  await f.detail(f.blocks[0], [{ id: 3, kind: 'handoff', songID: '42', title: '星河', position: 0, needBy: f.at, expiresAt: f.at + 30000, windowClosed: false }]);
  await expect(headings).toHaveCount(0);
  await expect(f.panel.locator('.demand-view-heading')).toHaveText('需求视图0 项需求');
  await expect(f.panel).toContainText('当前没有使用需求');
  await f.canvas.focus(); await f.canvas.press('Home');
  await expect(page.getByRole('group', { name: '块需求详情' })).toContainText('短期交接');
  await f.emit({ demands: [demand(7, f.blocks.slice(1), { kind: 'formal', playback: { zeroAt: f.at, endAt: f.at + 60000 } })] });
  await expect(headings).toHaveText([/正式播放/]);
});

test('an expired download keeps its original time on the timeline until the handoff ends', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page, 0);
  const deadline = f.at + 30000, expiresAt = deadline + 2000;
  const missing = block(15, f.at, { deadline, totalBlocks: 20, state: 'downloading' });
  const get = demand(1, [missing], { totalBlocks: 20 });
  await f.emit({ reset: true, blocks: [missing], demands: [get], used: 1 });
  await f.open();
  await expect(f.panel.locator('.demand-row')).toHaveCount(1);

  const handedOff = { ...missing, timeBasis: 'last-need-by' as const, needBy: 0, deadline: 0, eligible: false, state: 'waiting' as const };
  const handoff = demand(2, [handedOff], { kind: 'handoff', totalBlocks: 20 });
  await f.detail(handedOff, [{ id: 2, kind: 'handoff', songID: '42', title: missing.title, position: 0, needBy: f.at, expiresAt, windowClosed: true }]);
  await f.emit({ at: deadline, blocks: [handedOff], demands: [handoff], removedDemands: [get.id] });
  await expect(f.panel.locator('.demand-row')).toHaveCount(0);
  await expect(f.canvas).toHaveAccessibleName(/块需求时间轴，1 个块/);
  await f.canvas.click({ position: { x: await f.xAt(missing.at), y: singleY } });
  const popup = page.getByRole('group', { name: '块需求详情' });
  const original = await page.evaluate(at => new Date(at).toLocaleString('zh-CN', { hour12: false }), f.at);
  await expect(popup.locator('dt:text-is("原 need-by") + dd')).toHaveText(original);
  await expect(popup).toContainText('短期交接期间沿用原 need-by 的位置');
  await expect(f.panel.locator('.timeline-selection-status')).toContainText(`原 need-by ${original}`);

  await f.emit({ at: expiresAt, removed: [missing.id], removedDemands: [handoff.id], used: 0 });
  await expect(f.canvas).toBeHidden();
  await expect(popup).toHaveCount(0);
});

test('formal playback embeds the room clock and shows the union of game reads and their handoffs', async ({ page }, info) => {
  const f = await setup(page, 6);
  await page.clock.install();
  const totalBytes = 55;
  const blocks = f.blocks.map(b => ({
    ...b, totalBlocks: 6, byteOffset: b.block * 10, bytes: b.block === 5 ? 5 : 10,
    inUse: true, readers: b.block === 1 ? 1 : 0, state: b.block === 2 ? 'protected' as const : 'waiting' as const,
  }));
  const playback = { zeroAt: f.at - 60000, endAt: f.at + 120000 };
  const formal = demand(1, [blocks[0], blocks[1], blocks[5]], { kind: 'formal', totalBlocks: 6, playback });
  const local = { totalBytes, availableBytes: 10, availableRanges: [{ start: 20, end: 30 }] };
  const firstRead = demand(2, [blocks[1], blocks[3]], { totalBlocks: 6 });
  const secondRead = demand(3, [blocks[1]], { totalBlocks: 6 });
  const handoff = demand(4, [blocks[2]], { kind: 'handoff', gameSongID: '42', totalBlocks: 6 });
  const preview = demand(5, [blocks[4]], { gameSongID: undefined, totalBlocks: 6 });
  const queue = demand(6, [blocks[4]], { kind: 'queue', position: 1, totalBlocks: 6 });
  const manual = demand(7, [blocks[5]], { kind: 'batch', totalBlocks: 6 });
  const previewHandoff = demand(8, [blocks[4]], { kind: 'handoff', totalBlocks: 6 });
  const otherSongHandoff = demand(9, [blocks[4]], { kind: 'handoff', gameSongID: '84', totalBlocks: 6 });
  const directRead = demand(10, [blocks[4]], { gameSongID: undefined, totalBlocks: 6 });
  await f.detail(blocks[1], [formal, firstRead, secondRead].map(d => ({
    id: d.sourceID, kind: d.kind, songID: d.songID, title: d.title, position: 0, needBy: f.at, expiresAt: 0, windowClosed: false,
  })));
  await f.emit({ blocks, demands: [formal, firstRead, secondRead, handoff, preview, queue, manual, previewHandoff, otherSongHandoff, directRead], availability: { [formal.resource]: local }, used: blocks.length });
  const headings = f.panel.locator('.demand-row-heading');
  await expect(headings).toHaveText([/正式播放.*01:0\d \/ 03:00/, /读取 #5/, /读取 #10/, /队列 #1/, /手动下载/]);
  const row = f.panel.locator('.demand-row').first(), strip = row.locator('canvas');
  const progress = row.getByRole('progressbar', { name: /房间播放进度/ });
  const availability = row.locator('.demand-video-heading .demand-progress');
  const availableStops = () => progress.evaluate((el, bytes) => [...getComputedStyle(el).backgroundImage.matchAll(/([\d.]+)%/g)]
    .map(([, percent]) => Math.round(Number(percent) * bytes / 100)), totalBytes);
  await expect(strip).toHaveAccessibleName(/游戏读取与交接 3 个块，视频共 6 块/);
  await expect(f.panel.locator('.demand-video-heading .demand-progress')).toHaveText(Array(5).fill('18%'));
  await expect(availability).toHaveAttribute('aria-label', /视频本地可用比例 18%/);
  await expect(availability).toHaveAttribute('title', '整首视频中 10 / 55 字节本地可用');
  await expect.poll(availableStops).toEqual([20, 20, 30, 30]);
  await f.emit({
    blocks: [{ ...blocks[3], state: 'available' }],
    availability: { [formal.resource]: { ...local, availableBytes: 20, availableRanges: [{ start: 20, end: 40 }] } },
  });
  await expect(availability).toHaveText('36%');
  await expect.poll(availableStops).toEqual([20, 20, 40, 40]);
  const heights = await f.panel.locator('.demand-row').evaluateAll(rows => rows.map(row => row.getBoundingClientRect().height));
  expect(new Set(heights).size).toBe(1);
  const titleBounds = (await row.locator('.demand-video-title').boundingBox())!;
  const progressBounds = (await progress.boundingBox())!;
  const stripBounds = (await strip.boundingBox())!;
  expect(progressBounds.y).toBeGreaterThanOrEqual(titleBounds.y + titleBounds.height);
  expect(progressBounds.y + progressBounds.height).toBeLessThanOrEqual(stripBounds.y);
  const before = Number(await progress.getAttribute('aria-valuenow'));
  await page.clock.runFor(2100);
  await expect.poll(async () => Number(await progress.getAttribute('aria-valuenow'))).toBeGreaterThan(before + 1000);

  await strip.focus(); await strip.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup).toContainText('块 2/6');
  await expect(popup).toContainText('正式播放');
  await expect(popup).toContainText('播放请求 #2');
  await expect(popup).toContainText('播放请求 #3');
  await strip.press('ArrowRight'); await expect(popup).toContainText('块 3/6');
  await strip.press('End'); await expect(popup).toContainText('块 4/6');
  await strip.press('Escape');

  const availableRanges = [{ start: 20, end: 40 }, { start: 50, end: 55 }];
  await f.emit({ availability: { [formal.resource]: { ...local, availableBytes: 25, availableRanges } } });
  await expect.poll(availableStops).toEqual([20, 20, 40, 40, 50, 50, 55, 55]);
  await expect(availability).toHaveText('45%');
  await f.panel.screenshot({ path: info.outputPath('formal-playback-and-demands.png') });

  const corrected = { zeroAt: f.at - 30000, endAt: f.at + 150000 };
  await f.emit({ removedDemands: [firstRead.id, secondRead.id, handoff.id], demands: [{ ...formal, playback: corrected }] });
  await expect(strip).toHaveAccessibleName(/游戏读取与交接 0 个块/);
  await expect(availability).toHaveText('45%');
  await expect.poll(availableStops).toEqual([20, 20, 40, 40, 50, 50, 55, 55]);
  await expect(strip).toHaveAttribute('tabindex', '-1');
  await expect(progress).toHaveAttribute('aria-valuetext', /^00:3\d \/ 03:00$/);
  await f.emit({ availability: { [formal.resource]: { totalBytes, availableBytes: totalBytes, availableRanges: [{ start: 0, end: totalBytes }] } } });
  await expect.poll(availableStops).toEqual([0, 0, 55, 55]);
  await expect(availability).toHaveText('100%');
  await f.emit({ availability: { [formal.resource]: { totalBytes, availableBytes: 0 } } });
  await expect(progress).toHaveCSS('background-image', 'none');
  await expect(availability).toHaveText('0%');
  await f.emit({ removedAvailability: [formal.resource] });
  await expect(availability).toHaveCount(0);
  await f.emit({ removedDemands: [formal.id] });
  await expect(headings).toHaveText([/读取 #5/, /读取 #10/, /队列 #1/, /手动下载/]);
});

test('forced colors show the playback track before any blocks are ready', async ({ page }) => {
  await page.emulateMedia({ forcedColors: 'active' });
  const f = await setup(page, 1);
  await page.clock.install({ time: new Date(f.at) });
  await page.clock.pauseAt(new Date(f.at + 5000));
  const waiting = { ...f.blocks[0], state: 'waiting' as const };
  const playback = { zeroAt: f.at, endAt: f.at + 60000 };
  const totalBytes = waiting.bytes;
  const formal = demand(1, [], { kind: 'formal', playback });
  await f.emit({ blocks: [waiting], demands: [formal, demand(2, [waiting])], availability: { [formal.resource]: { totalBytes, availableBytes: 0 } } });

  const progress = f.panel.getByRole('progressbar', { name: /房间播放进度/ });
  const time = f.panel.getByText('00:00 / 01:00', { exact: true });
  await expect(progress).toHaveAttribute('aria-valuenow', '0');
  await expect(progress).toHaveCSS('background-image', 'none');
  const track = await progress.evaluate(el => {
    const { outlineColor, outlineStyle, outlineWidth } = getComputedStyle(el);
    const rowBackground = getComputedStyle(el.closest('.demand-row')!).backgroundColor;
    const marker = el.parentElement!.querySelector('.demand-playback-marker')!;
    return { outlineStyle, outlineWidth, contrastsWithRow: outlineColor !== rowBackground, markerMatchesOutline: getComputedStyle(marker, '::after').backgroundColor === outlineColor };
  });
  expect(track).toEqual({ outlineStyle: 'solid', outlineWidth: '1px', contrastsWithRow: true, markerMatchesOutline: true });
  await expect(time).toBeVisible();

  await f.emit({
    blocks: [{ ...waiting, state: 'available' }],
    availability: { [formal.resource]: { totalBytes, availableBytes: totalBytes, availableRanges: [{ start: 0, end: totalBytes }] } },
  });
  await expect(progress).toHaveCSS('background-image', /linear-gradient/);
  await expect(time).toBeVisible();
});

test('playback advances between seconds and only realigns for room clock corrections', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  const f = await setup(page, 0);
  await page.clock.install({ time: new Date(f.at) });
  await page.clock.pauseAt(new Date(f.at + 5000));
  const formal = demand(1, [], { kind: 'formal', playback: { zeroAt: f.at - 60500, endAt: f.at + 119500 } });
  await f.emit({ demands: [formal] });

  const progress = f.panel.getByRole('progressbar', { name: /房间播放进度/ });
  const track = f.panel.locator('.demand-playback-track');
  const fill = progress.locator('.demand-playback-fill');
  const marker = track.locator('.demand-playback-marker');
  const layerEnd = (layer = fill) => layer.evaluate(el => {
    const trackLeft = el.closest('.demand-playback-track')!.getBoundingClientRect().left;
    return el.getBoundingClientRect().right - trackLeft;
  });
  await expect(progress).toHaveAttribute('aria-valuetext', '01:00 / 03:00');
  const trackWidth = (await progress.boundingBox())!.width;
  expect((await fill.boundingBox())!.width).toBeCloseTo(trackWidth);
  expect(await marker.evaluate(el => el.closest('.demand-playback-clip'))).toBeNull();
  const initialEnd = await layerEnd();
  await expect.poll(layerEnd).toBeGreaterThan(initialEnd);
  await expect(progress).toHaveAttribute('aria-valuetext', '01:00 / 03:00');

  const animation = await fill.evaluateHandle(el => el.getAnimations()[0]);
  await f.emit({ at: f.at + 200, active: 2 });
  await expect(f.panel.locator('.block-demand-capacity')).toContainText('下载 2 /');
  expect(await fill.evaluate((el, current) => el.getAnimations()[0] === current, animation)).toBe(true);

  await f.emit({ at: f.at + 200, demands: [{ ...formal, playback: { zeroAt: f.at - 30000, endAt: f.at + 150000 } }] });
  await expect(progress).toHaveAttribute('aria-valuetext', '00:30 / 03:00');
  await expect.poll(layerEnd).toBeLessThan(initialEnd * 0.6);
  const starts = await track.evaluate(el => el.getAnimations({ subtree: true }).map(animation => animation.startTime));
  expect(starts).toHaveLength(2);
  expect(starts[0]).not.toBeNull();
  expect(starts[0]).toBe(starts[1]);
  await animation.dispose();

  await page.emulateMedia({ reducedMotion: 'reduce' });
  await expect.poll(() => track.evaluate(el => el.getAnimations({ subtree: true }).length)).toBe(0);
  await f.emit({ at: f.at, demands: [{ ...formal, playback: { zeroAt: f.at, endAt: f.at + 60500 } }] });
  await expect(progress).toHaveAttribute('aria-valuenow', '0');
  await expect.poll(layerEnd).toBeCloseTo(0);
  await expect.poll(() => layerEnd(marker)).toBeCloseTo(0);
  await f.emit({ at: f.at + 60500 });
  await expect(progress).toHaveAttribute('aria-valuenow', '60500');
  await expect.poll(layerEnd).toBeCloseTo(trackWidth);
  await expect.poll(() => layerEnd(marker)).toBeCloseTo(trackWidth);
});

test('playback progress holds its whole-second value across block events and applies clock corrections immediately', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page, 1);
  await page.clock.install({ time: new Date(f.at) });
  await page.clock.pauseAt(new Date(f.at + 5000));
  const formal = demand(1, [], { kind: 'formal', playback: { zeroAt: f.at - 60000, endAt: f.at + 120000 } });
  await f.emit({ demands: [formal] });
  const progress = f.panel.getByRole('progressbar', { name: /房间播放进度/ });
  await expect(progress).toHaveAttribute('aria-valuenow', '60000');

  for (const [offset, active] of [[200, 2], [700, 1]]) {
    await f.emit({ at: f.at + offset, active });
    await expect(f.panel.locator('.block-demand-capacity')).toContainText(`下载 ${active} /`);
    await expect(progress).toHaveAttribute('aria-valuenow', '60000', { timeout: 1000 });
  }

  await f.emit({ at: f.at + 1000, active: 0 });
  await expect(progress).toHaveAttribute('aria-valuenow', '61000');
  await f.emit({ at: f.at + 1200, demands: [{ ...formal, playback: { zeroAt: f.at - 30000, endAt: f.at + 150000 } }] });
  await expect(progress).toHaveAttribute('aria-valuenow', '31000');
  await expect(progress).toHaveAttribute('aria-valuetext', '00:31 / 03:00');
});

test('event deltas, overlap selection, source changes and untimed demands stay inspectable', async ({ page }) => {
  const f = await setup(page);
  const first = block(100, f.at), second = block(101, f.at + 1), untimed = block(102, 0, { opportunistic: true });
  await f.detail(first); await f.detail(second); await f.detail(untimed);
  await f.emit({ reset: true, blocks: [first, second, untimed], used: 3 }); await f.open();
  const rect = (await f.canvas.boundingBox())!;
  await page.mouse.move(rect.x + await f.xAt(first.at), rect.y + singleY);
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup).toContainText('重叠 2 块');
  await expect(popup).toContainText('块 101/?');
  await expect(popup).toContainText('播放请求 #1'); await expect(popup).toContainText('房间队列 · 第 3 首');
  await popup.getByRole('button', { name: '下一个重叠块' }).click();
  await expect(popup).toContainText('块 102/?');
  const complete = { ...first, state: 'available' as const, sources: 1 };
  await f.detail(complete, [{ id: 3, kind: 'batch', songID: '42', title: '手动测试', position: 0, needBy: 0, expiresAt: 0, windowClosed: false }]);
  await f.emit({ blocks: [complete], removed: [second.id], used: 2 });
  await expect(popup).toContainText('本地可用'); await expect(popup).toContainText('手动下载');
  await expect(popup).not.toContainText('重叠 2 块');
  await expect(popup).not.toContainText('播放请求 #1'); await expect(f.canvas).toHaveAccessibleName(/2 个块/);
  await popup.getByRole('button', { name: '关闭块详情' }).click(); await f.canvas.press('End');
  await expect(popup).toContainText('块 103/?'); await expect(popup).toContainText('无有限期限');
  await page.getByRole('button', { name: '关闭块详情' }).click();
  await f.emit({ removed: [first.id, untimed.id], used: 0 }); await expect(f.canvas).toHaveAttribute('aria-label', /0 个块/);
});

test('zoom preserves the follow cutoff or fixed center, dragging fixes the view and click pins details', async ({ page }, info) => {
  const f = await setup(page); await f.open();
  await f.panel.screenshot({ path: info.outputPath('timeline-overview.png') });
  await f.canvas.scrollIntoViewIfNeeded();
  await expect(f.panel.getByText('状态展示：更新中', { exact: true })).toBeVisible();
  const before = Number(await f.viewport.getAttribute('data-span')), rect = (await f.canvas.boundingBox())!;
  const center = () => f.viewport.getAttribute('data-center').then(Number);
  const expectCutoff = async (at: number) => {
    const { center, span } = await f.axis();
    expect(Math.abs(center - span / 2 - (at - 30000))).toBeLessThanOrEqual(1);
  };
  await expectCutoff(f.at);
  expect(await f.xAt(f.at + 750000)).toBeLessThan(rect.width - 24);
  await page.mouse.move(rect.x + rect.width * .75, rect.y + 80); await page.mouse.wheel(0, -240);
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  await expect.poll(async () => Number(await f.viewport.getAttribute('data-span'))).toBeLessThan(before);
  await expectCutoff(f.at);
  await f.panel.getByRole('button', { name: '放大时间轴' }).click();
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  await expectCutoff(f.at);
  await f.canvas.press('+');
  await expectCutoff(f.at);
  await f.canvas.click({ position: { x: rect.width / 2, y: 80 } });
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  await page.mouse.move(rect.x + rect.width / 2, rect.y + 80); await page.mouse.down(); await page.mouse.move(rect.x + rect.width / 2 + 100, rect.y + 80, { steps: 5 }); await page.mouse.up();
  await expect(f.viewport).toHaveAttribute('data-follow', 'false');
  const fixedCenter = await center(), fixedSpan = Number(await f.viewport.getAttribute('data-span'));
  await page.mouse.move(rect.x + rect.width * .75, rect.y + 80); await page.mouse.wheel(0, -120);
  await expect.poll(async () => Number(await f.viewport.getAttribute('data-span'))).toBeLessThan(fixedSpan);
  await expect(f.viewport).toHaveAttribute('data-follow', 'false');
  expect(Math.abs(await center() - fixedCenter)).toBeLessThanOrEqual(1000);
  await f.canvas.focus(); await f.canvas.press('+');
  await expect(f.viewport).toHaveAttribute('data-follow', 'false');
  expect(Math.abs(await center() - fixedCenter)).toBeLessThanOrEqual(1000);
  await f.panel.getByRole('button', { name: '跟随现在', pressed: false }).click();
  const current = Date.now(), currentBlock = block(100, current);
  await f.detail(currentBlock); await f.emit({ at: current, reset: true, blocks: [currentBlock], used: 1 });
  await expect(f.canvas).toHaveAccessibleName(/1 个块/);
  await expectCutoff(current);
  await f.canvas.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup).toContainText('块 101/?');
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  await f.canvas.press('Escape');
  await f.canvas.click({ position: { x: await f.xAt(current), y: singleY } });
  await expect(popup).toContainText('块 101/?');
  await page.mouse.move(1, 1); await expect(page.getByRole('button', { name: '固定块详情' })).toHaveCount(0);
  await expect(popup).toBeVisible();
  await f.canvas.press('Escape');
  await f.canvas.dispatchEvent('wheel', { deltaY: -4000 });
  await expect(f.viewport).toHaveAttribute('data-span', '31000');
  await expectCutoff(current);
  await expect(f.panel.getByRole('button', { name: '放大时间轴' })).toBeDisabled();
});

test('follow and layers toggle independently while zoom buttons act once per click', async ({ page }) => {
  const f = await setup(page); await f.open();
  const follow = f.panel.getByRole('button', { name: '跟随现在' });
  const layers = f.panel.getByRole('button', { name: '分层显示' });
  const zoomOut = f.panel.getByRole('button', { name: '缩小时间轴' });
  const zoomIn = f.panel.getByRole('button', { name: '放大时间轴' });
  await expect(follow).toHaveAttribute('aria-pressed', 'true');
  await expect(layers).toHaveAttribute('aria-pressed', 'false');
  await follow.click();
  await expect(follow).toHaveAttribute('aria-pressed', 'false');
  await expect(f.viewport).toHaveAttribute('data-follow', 'false');
  const fixed = await f.axis();
  await f.emit({ at: f.at + 60000, used: 17 });
  await expect(f.panel.locator('.block-demand-capacity')).toContainText('17 /');
  await layers.click();
  await expect(layers).toHaveAttribute('aria-pressed', 'true');
  await zoomOut.click();
  await expect(f.viewport).toHaveAttribute('data-span', String(Math.round(fixed.span * 1.5)));
  await zoomIn.click();
  await expect(f.viewport).toHaveAttribute('data-span', String(fixed.span));
  expect((await f.axis()).center).toBe(fixed.center);
  for (const button of [zoomOut, zoomIn]) expect(await button.getAttribute('aria-pressed')).toBeNull();
  await follow.press('Space');
  await expect(follow).toHaveAttribute('aria-pressed', 'true');
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  expect((await f.axis()).center).toBe(f.at + 60000 - 30000 + fixed.span / 2);
  await expect(layers).toHaveAttribute('aria-pressed', 'true');
  await layers.click();
  await expect(layers).toHaveAttribute('aria-pressed', 'false');
});

test('horizontal wheel pans without zooming or changing mode and releases its listener when paused', async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page); await f.open();
  const center = () => f.viewport.getAttribute('data-center').then(Number);
  const initialCenter = await center(), span = await f.viewport.getAttribute('data-span'), rect = (await f.canvas.boundingBox())!;
  await page.mouse.move(rect.x + rect.width / 2, rect.y + 80); await page.mouse.wheel(120, 0);
  await expect.poll(center).toBeGreaterThan(initialCenter);
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  await expect(f.viewport).toHaveAttribute('data-span', span!);
  const shiftedCenter = await center();
  await f.emit({ at: f.at + 10000 });
  await expect.poll(async () => Math.abs(await center() - shiftedCenter - 10000)).toBeLessThanOrEqual(1000);
  await page.mouse.wheel(-80, 0);
  await expect.poll(center).toBeLessThan(shiftedCenter + 10000);
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  await f.panel.getByRole('button', { name: '跟随现在', pressed: false }).click();
  const restoredCenter = f.at + 10000 - 30000 + Number(span) / 2;
  expect(Math.abs(await center() - restoredCenter)).toBeLessThanOrEqual(1);
  // Shift+wheel uses the same horizontal gesture on mice without a tilt wheel.
  await page.mouse.move(rect.x + rect.width / 2, rect.y + 80);
  await page.keyboard.down('Shift'); await page.mouse.wheel(0, 120); await page.keyboard.up('Shift');
  await expect.poll(center).toBeGreaterThan(restoredCenter);
  await expect(f.viewport).toHaveAttribute('data-span', span!);
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  await page.mouse.down(); await page.mouse.move(rect.x + rect.width / 2 + 80, rect.y + 80, { steps: 5 }); await page.mouse.up();
  await expect(f.viewport).toHaveAttribute('data-follow', 'false');
  const fixedCenter = await center();
  await page.mouse.wheel(120, 0);
  await expect.poll(center).toBeGreaterThan(fixedCenter);
  await expect(f.viewport).toHaveAttribute('data-follow', 'false');
  await expect(f.viewport).toHaveAttribute('data-span', span!);
  const pausedCenter = await center();
  await setPageHidden(page, true);
  await expect.poll(f.totalStreams).toBe(0);
  await f.canvas.dispatchEvent('wheel', { deltaX: 120 });
  await f.canvas.dispatchEvent('wheel', { deltaY: -120 });
  expect(await center()).toBe(pausedCenter);
  await expect(f.viewport).toHaveAttribute('data-span', span!);
  await setPageHidden(page, false);
  await expect(f.panel.getByText('状态展示：更新中', { exact: true })).toBeVisible();
  await f.panel.getByRole('button', { name: '跟随现在', pressed: false }).click();
  await f.panel.getByRole('button', { name: '分层显示' }).click();
  await page.mouse.move(rect.x + rect.width / 2, rect.y + 80); await page.mouse.wheel(rect.width / 2 - 10, 0);
  await expect(f.panel.getByRole('button', { name: '跟随现在', pressed: false })).toBeVisible();
  await f.panel.screenshot({ path: info.outputPath('transparent-lane-labels.png') });
});

type PaintedBlock = { x: number; y: number; width: number; height: number; color: string; imageID?: number };

async function observeTimelineBlocks(page: Page) {
  await page.addInitScript(() => {
    type Context = CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D;
    const painted: PaintedBlock[] = [], bitmaps = new WeakMap<OffscreenCanvas, PaintedBlock>();
    Object.assign(window, { timelineBlockPaint: painted });
    const paths = new WeakMap<Context, Omit<PaintedBlock, 'color' | 'imageID'>>();
    let nextID = 0;
    const isTimeline = (canvas: HTMLCanvasElement | OffscreenCanvas) => canvas instanceof HTMLCanvasElement && canvas.classList.contains('timeline-canvas');
    const trackStrokes = (prototype: Context) => {
      const { beginPath, roundRect, stroke } = prototype;
      prototype.beginPath = function (this: Context) { paths.delete(this); beginPath.call(this); };
      prototype.roundRect = function (this: Context, x, y, width, height, radii) {
        if (width <= 36 && width === height) paths.set(this, { x, y, width, height });
        roundRect.call(this, x, y, width, height, radii);
      };
      prototype.stroke = function (this: Context, ...args: [] | [Path2D]) {
        const path = paths.get(this);
        if (path) {
          const block = { ...path, color: String(this.strokeStyle) };
          if (this.canvas instanceof OffscreenCanvas) bitmaps.set(this.canvas, { ...block, imageID: ++nextID });
          else if (isTimeline(this.canvas)) painted.push(block);
          paths.delete(this);
        }
        Reflect.apply(stroke, this, args);
      };
    };
    trackStrokes(CanvasRenderingContext2D.prototype);
    trackStrokes(OffscreenCanvasRenderingContext2D.prototype);
    const { clearRect, drawImage } = CanvasRenderingContext2D.prototype;
    CanvasRenderingContext2D.prototype.clearRect = function (...args) {
      if (isTimeline(this.canvas)) painted.length = 0;
      return clearRect.apply(this, args);
    };
    CanvasRenderingContext2D.prototype.drawImage = function (image: CanvasImageSource, ...args: number[]) {
      if (isTimeline(this.canvas) && image instanceof OffscreenCanvas) {
        const block = bitmaps.get(image);
        if (block) painted.push({ ...block, x: args[0] + block.x, y: args[1] + block.y });
      }
      Reflect.apply(drawImage, this, [image, ...args]);
    };
  });
  return () => page.evaluate(() => (window as unknown as { timelineBlockPaint: PaintedBlock[] }).timelineBlockPaint);
}

test('fixed squares, three state layers and demand memberships share selection and one stream', async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const readStrokes = await observeTimelineBlocks(page);
  const f = await setup(page);
  const blocks = [
    block(0, f.at, { state: 'waiting' }), block(1, f.at, { state: 'downloading' }), block(2, f.at, { state: 'available' }),
    block(3, f.at + 150000, { songID: '84', resource: 'other-video', title: '远山', state: 'available' }),
    ...Array.from({ length: 60 }, (_, i) => block(i + 4, f.at + 200000 + i * 20000)),
  ];
  const manual = demand(1, blocks.filter(b => b.resource === 'fixture-resource'), { kind: 'batch' }), queue2 = demand(2, [blocks[3]], { kind: 'queue', position: 2, songID: '84', resource: 'other-video', title: '远山' });
  const get = demand(4, blocks.slice(0, 3), { totalBlocks: 64 }), queue1 = demand(3, blocks.slice(0, 3), { kind: 'queue', position: 1, totalBlocks: 64 });
  for (const b of [...blocks.slice(0, 4), blocks.at(-1)!]) await f.detail(b);
  await f.emit({ reset: true, blocks, used: blocks.length, active: 1, demands: [manual, queue2, queue1, get] });
  await f.open();
  const rows = f.panel.locator('.demand-row'), headings = f.panel.locator('.demand-row-heading');
  await expect(headings).toHaveText([/读取 #4.*星河.*3 块/, /队列 #2.*远山.*1 块/, /手动下载.*星河.*63 块/]);
  const borders = await rows.evaluateAll(els => els.map(el => getComputedStyle(el).borderLeftColor));
  expect(borders[0]).toBe(borders[2]); expect(borders[0]).not.toBe(borders[1]);
  await expect.poll(f.totalStreams).toBe(1);
  await f.panel.getByRole('button', { name: '分层显示' }).click();
  await expect(f.viewport).toHaveAttribute('data-layers', '3');
  const popup = page.getByRole('group', { name: '块需求详情' });
  const rect = (await f.canvas.boundingBox())!;
  for (const [i, y] of [0, 1, 2].map(row => laneCenter(row, 16)).entries()) {
    await f.canvas.click({ position: { x: await f.xAt(f.at), y } });
    await expect(popup).toContainText(`块 ${i + 1}/`);
    await expect(popup).toContainText('星河 · 测试歌曲');
    await expect(popup).not.toContainText('重叠');
    await popup.getByRole('button', { name: '关闭块详情' }).click();
  }
  // The same source color survives state changes. Every painted block is square.
  await expect(f.panel.getByRole('slider')).toHaveCount(0);
  await expect(f.viewport).toHaveAttribute('data-block-size', '16');
  await expect.poll(async () => { const values = await readStrokes(); return values.length >= 3 && values.every(s => s.width === 16 && s.height === 16); }).toBe(true);
  const strokes = await readStrokes();
  expect(new Set(strokes.slice(-3).map(s => s.color)).size).toBe(1);
  await f.panel.screenshot({ path: info.outputPath('three-layers-and-demands.png') });
  const strip = rows.nth(2).locator('canvas');
  await strip.focus(); await strip.press('End');
  await expect(popup).toContainText('块 64/'); await expect.poll(f.totalStreams).toBe(2);
  expect(await rows.nth(2).locator('.demand-block-strip').evaluate(el => el.scrollWidth === el.clientWidth && el.scrollLeft === 0)).toBe(true);
  await strip.press('Escape'); await expect(popup).toHaveCount(0);
  await f.emit({ demands: [{ ...queue2, position: 1 }], removedDemands: [queue1.id, manual.id] });
  await expect(headings).toHaveText([/读取 #4/, /队列 #1.*远山/]);
  await expect.poll(f.totalStreams).toBe(1);
  await rows.first().locator('canvas').hover({ position: { x: 25, y: 25 } });
  await expect(popup).toContainText('块 1/');
  await expect.poll(f.totalStreams).toBe(2);
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  const untimed = blocks.slice(0, 3).map(b => ({ ...b, at: 0, needBy: 0, deadline: 0, timeBasis: 'untimed' as const }));
  for (const b of untimed) await f.detail(b);
  await f.emit({ reset: true, blocks: untimed, demands: [demand(1, untimed, { kind: 'batch' })], used: 3 });
  await expect(f.canvas).toHaveAccessibleName(/3 个块，单层/);
  const untimedRow = untimedLayout(rect.width, untimed.length, 16);
  for (let i = 0; i < untimed.length; i++) {
    await f.canvas.click({ position: { x: untimedRow.start + i * untimedRow.pitch + 8, y: timelineLayout(true, false, true, 16).untimedY + 8 } });
    await expect(popup).toContainText(`块 ${i + 1}/`);
    await expect(popup).toContainText('无有限期限');
    await popup.getByRole('button', { name: '关闭块详情' }).click();
  }
  await f.panel.getByRole('button', { name: '收起时间轴视图' }).click(); await expect.poll(f.totalStreams).toBe(1);
});

for (const reducedMotion of ['reduce', 'no-preference'] as const) test(`demand list grows with the page and live updates avoid blank frames with motion ${reducedMotion}`, async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 1400 });
  await page.emulateMedia({ reducedMotion });
  const f = await setup(page, 2);
  const blocks = f.blocks.map(b => ({ ...b, state: 'available' as const, inUse: true, totalBlocks: 2 }));
  const rows = Array.from({ length: 30 }, (_, i) => demand(i + 1, blocks, { kind: 'queue', position: i + 1 }));
  await f.emit({ reset: true, blocks, demands: rows, used: 2 }); await f.open();
  const list = f.panel.getByRole('list', { name: '视频需求列表' });
  await list.locator('.demand-row').first().scrollIntoViewIfNeeded();
  const rowBounds = () => list.evaluate(list => {
    const view = list.closest('.block-demand-view')!.getBoundingClientRect(), row = list.querySelector('.demand-row')!.getBoundingClientRect(), slot = list.querySelector('.demand-row-slot')!.getBoundingClientRect();
    return { width: row.width, height: list.clientHeight, rowHeight: slot.height, gapDifference: Math.abs(row.left - view.left - (view.right - row.right)), overflowing: list.scrollHeight > list.clientHeight, overflowY: getComputedStyle(list).overflowY };
  });
  const full = await rowBounds();
  expect(full.overflowing).toBe(false);
  expect(full.overflowY).toBe('visible');
  expect(full.height).toBe(30 * full.rowHeight);
  expect(full.gapDifference).toBeLessThanOrEqual(1);
  await expect(list.getByRole('listitem')).toHaveCount(30);
  await page.evaluate(() => {
    const samples: { rows: number; blank: number; wrongWidth: number }[] = [];
    let frame = 0;
    const sample = () => {
      const list = document.querySelector('.demand-list')!, rect = list.getBoundingClientRect();
      const canvases = [...list.querySelectorAll<HTMLCanvasElement>('canvas')].filter(canvas => {
        const bounds = canvas.getBoundingClientRect();
        return bounds.bottom > Math.max(rect.top, 0) && bounds.top < Math.min(rect.bottom, innerHeight);
      });
      samples.push({
        rows: list.querySelectorAll('.demand-row').length,
        blank: canvases.filter(canvas => !canvas.width || !canvas.height || !canvas.getContext('2d')!.getImageData(0, 0, canvas.width, canvas.height).data.some((channel, i) => i % 4 === 3 && channel > 0)).length,
        wrongWidth: canvases.filter(canvas => Math.abs(canvas.getBoundingClientRect().width - canvas.parentElement!.clientWidth) > 1 || canvas.width !== Math.round(canvas.parentElement!.clientWidth * devicePixelRatio)).length,
      });
      frame = requestAnimationFrame(sample);
    };
    frame = requestAnimationFrame(sample);
    Object.assign(window, { stopDemandFrames: () => { cancelAnimationFrame(frame); return samples; } });
  });
  // New GET sources replace one another while the queue stays in place.
  let previous: BlockDemand | undefined;
  for (let i = 0; i < 8; i++) {
    const next = demand(100 + i, blocks);
    await f.emit({ demands: [next], removedDemands: previous ? [previous.id] : [], used: 2 });
    await expect(list.locator('.demand-row-heading').first()).toContainText(`读取 #${next.sourceID}`);
    previous = next;
  }
  const last = list.locator('.demand-row').last();
  await last.scrollIntoViewIfNeeded();
  await expect(last).toBeInViewport();
  await expect(last).toContainText('队列 #30');
  await expect.poll(() => last.locator('canvas').evaluate(canvas => canvas.width)).toBeGreaterThan(0);
  await expect.poll(() => list.locator('canvas').first().evaluate(canvas => canvas.width)).toBe(0);
  expect(await page.evaluate(() => scrollY)).toBeGreaterThan(0);
  expect(await list.evaluate(list => list.scrollTop)).toBe(0);
  await page.screenshot({ path: info.outputPath('page-scroll-demand-list.png') });
  // Removing most rows lets the browser shrink the page and restore its scroll.
  await f.emit({ removedDemands: [...rows.slice(3).map(row => row.id), previous!.id], used: 2 });
  await expect(list.locator('.demand-source')).toHaveText(['队列 #1', '队列 #2', '队列 #3']);
  await expect.poll(() => page.evaluate(() => scrollY)).toBe(0);
  const compact = await rowBounds();
  expect(compact.overflowing).toBe(false);
  expect(compact.height).toBe(3 * compact.rowHeight);
  expect(compact.gapDifference).toBeLessThanOrEqual(1);
  expect(compact.width).toBe(full.width);
  const samples = await page.evaluate(() => (window as unknown as { stopDemandFrames: () => { rows: number; blank: number; wrongWidth: number }[] }).stopDemandFrames());
  await writeFile(info.outputPath('demand-frames.json'), JSON.stringify(samples, null, 2));
  expect(samples.length).toBeGreaterThan(0);
  expect(samples.filter(sample => !sample.rows || sample.blank || sample.wrongWidth)).toEqual([]);
  await page.setViewportSize({ width: 940, height: 1400 });
  await expect.poll(() => list.locator('canvas').first().evaluate(el => el.width === Math.round(el.parentElement!.clientWidth * devicePixelRatio))).toBe(true);
  await list.screenshot({ path: info.outputPath('updated-demand-list.png') });
  await f.panel.getByRole('button', { name: '收起时间轴视图' }).click();
  await expect.poll(f.totalStreams).toBe(1);
});

test('protection-only blocks are muted and translucent, and sparse demands keep their positions', async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 1100 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page);
  const protectedBlock = block(0, f.at - 18000, { state: 'protected', inUse: false, protected: true, sources: 0, needBy: 0, deadline: 0, timeBasis: 'ended', protectionUntil: f.at + 45000 });
  const middle = block(9, f.at + 150000, { state: 'available' }), later = block(15, f.at + 300000, { state: 'waiting' });
  const first = demand(1, [middle, later], { totalBlocks: 20 }), second = demand(2, [later], { kind: 'queue', position: 2, totalBlocks: 20 });
  for (const b of [protectedBlock, middle, later]) await f.detail(b, b.inUse ? undefined : []);
  await f.emit({ reset: true, blocks: [protectedBlock, middle, later], demands: [first, second], used: 2, active: 0 });
  await f.open();
  const pixels = await f.canvas.evaluate((c, { y, backgroundY, protectedX, availableX }) => {
    const canvas = c as HTMLCanvasElement, ctx = canvas.getContext('2d')!;
    const sample = (x: number, y: number) => [...ctx.getImageData(Math.round(x * devicePixelRatio), Math.round(y * devicePixelRatio), 1, 1).data];
    return { protected: sample(protectedX, y), background: sample(protectedX, backgroundY), available: sample(availableX, y) };
  }, {
    y: singleY, backgroundY: timelineLayout(false, true, false, 16).plotTop + 4,
    protectedX: await f.xAt(protectedBlock.at), availableX: await f.xAt(middle.at),
  });
  const [red, green, blue] = pixels.available;
  const gray = .2126 * red + .7152 * green + .0722 * blue;
  for (let channel = 0; channel < 3; channel++) {
    const muted = gray + (pixels.available[channel] - gray) * .3;
    expect(Math.abs(pixels.protected[channel] - (.25 * muted + .75 * pixels.background[channel]))).toBeLessThanOrEqual(2);
  }
  const strips = f.panel.locator('.demand-block-canvas'), popup = page.getByRole('group', { name: '块需求详情' });
  await f.canvas.click({ position: { x: await f.xAt(protectedBlock.at), y: singleY } });
  const endedAt = await page.evaluate(at => new Date(at).toLocaleString('zh-CN', { hour12: false }), protectedBlock.at);
  await expect(popup).toContainText(`需求于 ${endedAt} 结束`);
  await expect(popup).toContainText('此前的 need-by 未记录');
  await f.canvas.press('Escape');
  await expect(strips.first()).toHaveAccessibleName(/需要 2 个块，视频共 20 块/);
  const width = await strips.first().evaluate(c => c.clientWidth), x = (index: number) => 20 + index * (width - 40) / 19;
  const alphaAt = (row: number, index: number) => strips.nth(row).evaluate((c, at) => {
    const pixel = (c as HTMLCanvasElement).getContext('2d')!.getImageData(Math.round(at * devicePixelRatio), 20 * devicePixelRatio, 1, 1).data;
    return pixel[3];
  }, x(index));
  expect(await alphaAt(0, 0)).toBe(0); expect(await alphaAt(0, 12)).toBe(0);
  expect(await alphaAt(0, 9)).toBe(255); expect(await alphaAt(0, 15)).toBe(255); expect(await alphaAt(1, 15)).toBe(255);
  await strips.first().click({ position: { x: x(0), y: 20 } }); await expect(popup).toHaveCount(0);
  await strips.first().hover({ position: { x: x(9), y: 20 } }); await expect(popup).toContainText('块 10/');
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  await strips.nth(1).click({ position: { x: x(15), y: 20 } }); await expect(popup).toContainText('块 16/');
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  await f.panel.screenshot({ path: info.outputPath('sparse-demand-positions.png') });
  await f.emit({ removed: [middle.id], demands: [{ ...first, blocks: [later.id] }], used: 1 });
  await expect(strips.first()).toHaveAccessibleName(/需要 1 个块，视频共 20 块/);
  await expect.poll(() => alphaAt(0, 9)).toBe(0);
  expect(await alphaAt(0, 15)).toBe(255);
  await strips.first().focus(); await strips.first().press('Home'); await expect(popup).toContainText('块 16/');
  await strips.first().press('Escape');
  await f.panel.getByRole('button', { name: '收起时间轴视图' }).click(); await expect.poll(f.totalStreams).toBe(1);
});

test('demand rows use the saturation-capped tab10 palette in both themes', async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 980 });
  await page.emulateMedia({ colorScheme: 'light' });
  const f = await setup(page, 0);
  const songs = ['4201', '4209', '4202', '4203', '4204', '4205', '4206', '4207', '4208'];
  const blocks = songs.map((songID, i) => block(i, f.at, { block: 0, songID, resource: `video-${i}`, state: 'available', totalBlocks: 1 }));
  const demands = blocks.map((b, i) => demand(i + 1, [b], { kind: 'queue', position: i + 1, songID: b.songID, resource: b.resource }));
  await f.emit({ blocks, demands });
  const rows = f.panel.locator('.demand-row');
  const borders = () => rows.evaluateAll(items => items.map(row => getComputedStyle(row).borderTopColor));
  const cappedTab10 = [
    'rgb(59, 114, 152)', 'rgb(188, 131, 81)', 'rgb(57, 147, 57)', 'rgb(183, 70, 71)', 'rgb(148, 103, 189)',
    'rgb(140, 86, 75)', 'rgb(209, 137, 187)', 'rgb(160, 161, 62)', 'rgb(64, 157, 166)',
  ];
  await expect(rows.first()).toHaveCSS('background-color', 'rgb(255, 255, 255)');
  await expect.poll(borders).toEqual(cappedTab10);
  const firstBlock = rows.first().locator('canvas');
  await expect.poll(() => firstBlock.evaluate(el => {
    const canvas = el as HTMLCanvasElement, ctx = canvas.getContext('2d')!;
    return [...ctx.getImageData(Math.floor(canvas.width / 2), Math.round(20 * devicePixelRatio), 1, 1).data];
  })).toEqual([59, 114, 152, 255]);
  await f.panel.screenshot({ path: info.outputPath('tab10-light.png') });
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(rows.first()).toHaveCSS('background-color', 'rgb(41, 41, 41)');
  await expect.poll(borders).toEqual(cappedTab10);
  await f.panel.screenshot({ path: info.outputPath('tab10-dark.png') });
  await page.emulateMedia({ forcedColors: 'active' });
  await expect.poll(async () => new Set(await borders()).size).toBe(1);
});

test('video colors survive hidden handoff and reconnect with full video numbering', async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 1100 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page);
  const first = block(12, f.at + 120000, { state: 'available', totalBlocks: 45 });
  const second = block(13, f.at + 128000, { state: 'available', totalBlocks: 45 });
  const queue = demand(1, [first, second], { kind: 'queue', position: 1, totalBlocks: 45 });
  await f.detail(first); await f.detail(second);
  await f.emit({ reset: true, blocks: [first, second], demands: [queue], used: 2 }); await f.open();
  const row = f.panel.locator('.demand-row').first(), strip = row.locator('canvas');
  const border = await row.evaluate(el => getComputedStyle(el).borderLeftColor);
  const sample = async () => f.canvas.evaluate((c, { x, y }) => {
    const canvas = c as HTMLCanvasElement, ctx = canvas.getContext('2d')!;
    const pixel = (at: number) => [...ctx.getImageData(Math.round(at * devicePixelRatio), y * devicePixelRatio, 1, 1).data];
    // The subpixel edge can straddle two columns as the clock advances.
    const edge = [7, 8, 9].map(offset => pixel(x + offset)).sort((a, b) => a[0] + a[1] + a[2] - b[0] - b[1] - b[2])[0];
    return { fill: pixel(x - 2), edge };
  }, { x: await f.xAt(first.at), y: singleY });
  const original = await sample();
  // The overlapping front square has a darker edge than its same-hue fill.
  expect(original.edge.slice(0, 3).every((value, i) => value <= original.fill[i])).toBe(true);
  expect(original.edge.slice(0, 3).some((value, i) => value < original.fill[i] - 3)).toBe(true);
  const handoff = demand(2, [first, second], { kind: 'handoff', songID: '', totalBlocks: 45 });
  const changed = [first, second].map(b => ({ ...b, songID: '', sources: 1 }));
  await f.detail(changed[0], [{ id: 2, kind: 'handoff', songID: '42', title: '星河 · 测试歌曲', position: 0, needBy: first.needBy, expiresAt: f.at + 30000, windowClosed: false }]);
  await f.emit({ blocks: changed, demands: [handoff], removedDemands: [queue.id], used: 2, active: 0 });
  await expect(row).toHaveCount(0);
  await expect(f.panel).toContainText('当前没有使用需求');
  expect((await sample()).fill).toEqual(original.fill);
  await f.canvas.focus(); await f.canvas.press('Home');
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup).toContainText('块 13/45');
  await expect(popup).toContainText('星河 · 测试歌曲');
  await expect(popup).toContainText('交接截止');
  await f.panel.screenshot({ path: info.outputPath('handoff-and-numbering.png') });
  await f.canvas.press('Escape');
  await f.panel.getByText('需求视图', { exact: true }).click();
  await f.panel.screenshot({ path: info.outputPath('handoff-overview.png') });
  await page.request.post('/__fixture/disconnect', { data: { id: f.fixtureID, kind: 'blocks' } });
  await expect(f.panel).toContainText('状态展示：重连中');
  // A reset with changed identity must keep the established video color too.
  await f.emit({ reset: true, blocks: changed.map(b => ({ ...b, songID: '84' })), demands: [{ ...queue, songID: '84' }], used: 2 });
  await expect(f.panel.getByText('状态展示：更新中', { exact: true })).toBeVisible();
  await expect(row).toHaveCSS('border-left-color', border);
  const stripFill = () => strip.evaluate(c => [...(c as HTMLCanvasElement).getContext('2d')!.getImageData(Math.round((20 + 12 * (c.clientWidth - 40) / 44) * devicePixelRatio), 20 * devicePixelRatio, 1, 1).data]);
  await expect.poll(stripFill).toEqual(original.fill);
  expect((await sample()).fill).toEqual(original.fill);
});

test('fitted demand blocks keep earlier positions on top in both views', async ({ page }, info) => {
  // Keep the fitted pitch below the 16px block size so these blocks overlap.
  await page.setViewportSize({ width: 900, height: 1100 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page);
  const first = block(0, f.at + 120000, { totalBlocks: 64 });
  const second = block(1, f.at + 128000, { state: 'available', totalBlocks: 64 });
  const last = block(63, f.at + 300000, { state: 'available', totalBlocks: 64 });
  const blocks = [last, second, first];
  for (const b of blocks) await f.detail(b);
  await f.emit({ reset: true, blocks, used: 3, demands: [
    demand(1, blocks, { kind: 'batch' }), demand(2, blocks, { kind: 'queue', position: 1 }),
    demand(3, blocks, { kind: 'handoff' }), demand(4, blocks),
  ] });
  await f.open();
  await expect(f.panel.locator('.demand-row-heading')).toHaveText([/读取 #4/, /手动下载/]);
  const popup = page.getByRole('group', { name: '块需求详情' });
  const timelinePixels = async () => f.canvas.evaluate((c, { y, firstX, secondX }) => {
    const ctx = (c as HTMLCanvasElement).getContext('2d')!;
    const overlapX = (firstX + secondX) / 2;
    const at = (x: number) => [...ctx.getImageData(Math.round(x * devicePixelRatio), y * devicePixelRatio, 1, 1).data];
    return { early: at(firstX - 3), overlap: at(overlapX), late: at(secondX + 6), x: overlapX };
  }, { y: singleY, firstX: await f.xAt(first.at), secondX: await f.xAt(second.at) });
  const timeline = await timelinePixels();
  const scrollBeforeDetails=await page.evaluate(()=>scrollY);
  expect(timeline.overlap).toEqual(timeline.early); expect(timeline.overlap).not.toEqual(timeline.late);
  await f.canvas.click({ position: { x: timeline.x, y: singleY } });
  await expect(popup).toContainText('块 1/64');
  await popup.getByRole('button', { name: '下一个重叠块' }).click();
  await expect(popup).toContainText('块 2/64');
  expect((await timelinePixels()).overlap).toEqual(timeline.overlap);
  await expect.poll(()=>page.evaluate(()=>scrollY)).toBe(scrollBeforeDetails);
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  const strip = f.panel.locator('.demand-block-canvas').first();
  await expect.poll(() => strip.evaluate(c => {
    const canvas = c as HTMLCanvasElement;
    return c.clientWidth === c.parentElement!.clientWidth && canvas.getContext('2d')!.getImageData(Math.round((c.clientWidth - 20) * devicePixelRatio), 20 * devicePixelRatio, 1, 1).data[3] === 255;
  })).toBe(true);
  const demandPixels = () => strip.evaluate(c => {
    const ctx = (c as HTMLCanvasElement).getContext('2d')!, pitch = (c.clientWidth - 40) / 63;
    const overlapX = (12 + pitch + 28) / 2;
    // Sample inside the shared fill, away from the first square's right stroke.
    const at = (x: number) => [...ctx.getImageData(Math.floor(x * devicePixelRatio), 20 * devicePixelRatio, 1, 1).data];
    return { early: at(20), overlap: at(overlapX), late: at(20 + pitch), x: overlapX };
  });
  const demandPaint = await demandPixels();
  expect(demandPaint.overlap).toEqual(demandPaint.early); expect(demandPaint.overlap).not.toEqual(demandPaint.late);
  await strip.click({ position: { x: demandPaint.x, y: 20 } });
  await expect(popup).toContainText('块 1/64'); await expect(popup).toContainText('重叠 2 块');
  await popup.getByRole('button', { name: '下一个重叠块' }).click();
  await expect(popup).toContainText('块 2/64');
  expect((await demandPixels()).overlap).toEqual(demandPaint.overlap);
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  await f.panel.screenshot({ path: info.outputPath('fitted-demand-overlap.png') });
  await page.setViewportSize({ width: 390, height: 1000 });
  await expect(f.viewport).toHaveAttribute('data-block-size', '16');
  await expect.poll(() => strip.evaluate(c => c.clientWidth === c.parentElement!.clientWidth && c.height === 34 * devicePixelRatio)).toBe(true);
  expect(await strip.locator('..').evaluate(el => el.scrollWidth === el.clientWidth)).toBe(true);
  const width = await strip.evaluate(c => c.clientWidth);
  // Both endpoints stay fully visible after a narrow resize, with no horizontal scroll.
  await strip.click({ position: { x: width - 20, y: 20 } });
  await expect(popup).toContainText('块 64/64');
  await strip.press('Escape'); await strip.press('Home');
  await expect(popup).toContainText('块 1/64');
  await strip.press('Escape');
  await f.panel.screenshot({ path: info.outputPath('fitted-demand-narrow.png') });
});

for (const view of ['timeline', 'demand'] as const) test(`hover refreshes the full overlap stack and top block in ${view}, while pinned details stay fixed`, async ({ page }, info) => {
  await page.setViewportSize({ width: view === 'demand' ? 900 : 1280, height: 1100 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page, 0);
  const first = block(0, f.at + 120000, { state: 'available', totalBlocks: 64 });
  const second = block(1, f.at + 128000, { state: 'waiting', totalBlocks: 64 });
  await f.detail(first); await f.detail(second);
  await f.emit({ reset: true, blocks: [first, second], demands: [demand(1, [first, second], { totalBlocks: 64 })], used: 2 });
  await f.open();
  const target = view === 'timeline' ? f.canvas : f.panel.locator('.demand-block-canvas');
  await target.scrollIntoViewIfNeeded();
  const rect = (await target.boundingBox())!;
  const firstCenter = await f.xAt(first.at);
  const secondCenter = await f.xAt(second.at);
  const pitch = (rect.width - 40) / 63;
  const firstOnly = view === 'timeline' ? firstCenter - 6 : 16;
  const secondOnly = view === 'timeline' ? secondCenter + 6 : 12 + pitch + 15;
  const overlap = view === 'timeline' ? firstCenter + 3 : 13 + pitch;
  const y = view === 'timeline' ? singleY : 20;
  // Wait for hit-target actionability while async source details reposition
  // the popover; raw mouse moves can land on that surface during its resize.
  const move = (x: number) => target.hover({ position: { x, y } });
  const popup = page.getByRole('group', { name: '块需求详情' });
  await move(firstOnly);
  await expect(popup).toContainText('块 1/64'); await expect(popup).not.toContainText('重叠');
  await move(overlap);
  await expect(popup).toContainText('重叠 2 块'); await expect(popup).toContainText('块 1/64');
  await expect(popup.getByRole('button', { name: '下一个重叠块' })).toBeVisible();
  await popup.screenshot({ path: info.outputPath(`hover-overlap-${view}.png`) });
  await move(firstOnly);
  await expect(popup).not.toContainText('重叠'); await expect(popup).toContainText('块 1/64');
  await move(secondOnly);
  await expect(popup).toContainText('块 2/64'); await expect(popup).not.toContainText('重叠');
  await move(overlap);
  await expect(popup).toContainText('重叠 2 块'); await expect(popup).toContainText('块 1/64');
  // Explicitly choosing a covered block pins the full stack despite later movement.
  await popup.getByRole('button', { name: '下一个重叠块' }).click();
  await expect(popup).toContainText('块 2/64');
  await move(firstOnly);
  await expect(popup).toContainText('块 2/64'); await expect(popup).toContainText('重叠 2 块');
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  // Clicking a single block likewise keeps its detail fixed on entering an overlap.
  await target.click({ position: { x: secondOnly, y } });
  await expect(popup).toContainText('块 2/64'); await expect(popup).not.toContainText('重叠');
  await move(overlap);
  await expect(popup).toContainText('块 2/64'); await expect(popup).not.toContainText('重叠');
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  await expect.poll(f.totalStreams).toBe(1);
});

test('WebSocket reconnect resets old state without a polling endpoint', async ({ page }) => {
  const f = await setup(page); await f.open();
  await page.request.post('/__fixture/disconnect', { data: { id: f.fixtureID, kind: 'blocks' } });
  await expect(f.panel).toContainText('状态展示：重连中');
  await f.emit({ reset: true, blocks: [block(100, f.at)], used: 1 });
  await expect(f.canvas).toHaveAccessibleName(/1 个块/); await expect(f.panel.getByText('状态展示：更新中', { exact: true })).toBeVisible();
  expect(f.calls.filter(c => c.path === 'block-demands/events').length).toBe(2);
});

test('opening before engine startup waits for its event on the same connection', async ({ page }) => {
  const f = await setup(page);
  await f.emit({ reset: true, running: false, blocks: [], used: 0 });
  await f.panel.getByRole('button', { name: '展开时间轴视图' }).click();
  await expect(f.panel.getByText('状态展示：待启动', { exact: true })).toBeVisible();
  await expect.poll(f.totalStreams).toBe(1);
  await f.emit({ reset: true, blocks: f.blocks, used: 44 });
  await expect(f.panel.getByText('状态展示：更新中', { exact: true })).toBeVisible();
  await expect(f.canvas).toHaveAccessibleName(/48 个块/);
  expect(f.calls.filter(c => c.path === 'block-demands/events')).toHaveLength(1);
});

for (const theme of ['light', 'dark', 'contrast'] as const) test(`timeline is readable at narrow widths in ${theme}`, async ({ page }, info) => {
  await page.setViewportSize({ width: theme === 'light' ? 320 : 390, height: 844 });
  await page.emulateMedia({ colorScheme: theme === 'dark' ? 'dark' : 'light', forcedColors: theme === 'contrast' ? 'active' : 'none', reducedMotion: 'reduce' });
  const f = await setup(page); await f.open();
  await f.emit({ demands: [demand(1, f.blocks.filter(b => b.inUse)), demand(2, f.blocks.filter(b => b.inUse), { kind: 'queue', position: 1 })] });
  await f.panel.getByRole('button', { name: '分层显示' }).click();
  await expect(f.canvas).toHaveAccessibleName(/三层/);
  await expect(f.panel.locator('.timeline-help')).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await f.panel.screenshot({ path: info.outputPath(`timeline-${theme}.png`) });
  await f.canvas.focus(); await f.canvas.press('ArrowRight');
  const popup = page.getByRole('group', { name: '块需求详情' }); await expect(popup).toBeVisible();
  expect(await popup.evaluate(el => el.getBoundingClientRect().right <= innerWidth)).toBe(true);
  await popup.screenshot({ path: info.outputPath(`detail-${theme}.png`) });
  await page.keyboard.press('Escape'); await expect(popup).toHaveCount(0);
});

test('event updates preserve unchanged collections and immutable previous snapshots', () => {
  const first = block(0, 1000), row = demand(1, [first], { kind: 'queue', position: 2 });
  const event: BlockEvent = { at: 1000, running: true, reset: true, capacity: 4096, used: 1, active: 0, open: 1, prepareAheadMS: 500000, downloadGraceMS: 30000, blocks: [first], removed: [], demands: [row], removedDemands: [] };
  const initial = applyBlockEvent(undefined, event);
  const delta = { ...event, reset: false, blocks: [], demands: [] };
  const capacity = applyBlockEvent(initial, { ...delta, open: 2 });
  expect(capacity.open).toBe(2);
  expect(capacity.blocks).toBe(initial.blocks); expect(capacity.demands).toBe(initial.demands);
  expect(capacity.colorPool).toBe(initial.colorPool);
  const reordered = applyBlockEvent(capacity, { ...delta, demands: [{ ...row, position: 1, songID: '84' }] });
  expect(reordered.blocks).toBe(initial.blocks);
  expect(reordered.demands.get(row.id)?.position).toBe(1);
  expect(initial.demands.get(row.id)?.position).toBe(2);
  expect(reordered.colorPool).toBe(initial.colorPool);
  const downloaded = applyBlockEvent(reordered, { ...delta, blocks: [{ ...first, state: 'available' }] });
  expect(downloaded.demands).toBe(reordered.demands);
  expect(downloaded.blocks.get(first.id)?.state).toBe('available');
  expect(initial.blocks.get(first.id)?.state).toBe('waiting');
  expect(downloaded.colorPool).toBe(initial.colorPool);
  const removed = applyBlockEvent(downloaded, { ...delta, removed: [first.id], removedDemands: [row.id] });
  expect(removed.blocks.size).toBe(0); expect(removed.demands.size).toBe(0);
  expect(downloaded.blocks.size).toBe(1); expect(downloaded.demands.size).toBe(1);
  const reset = applyBlockEvent(downloaded, { ...delta, reset: true });
  expect(reset.blocks.size).toBe(0); expect(reset.demands.size).toBe(0);

  const formal = demand(2, [], { kind: 'formal', playback: { zeroAt: 1000, endAt: 61000 } });
  const room = applyBlockEvent(undefined, { ...event, blocks: [], demands: [formal] });
  const shared = applyBlockEvent(room, { ...delta, blocks: [{ ...first, songID: '84' }], demands: [formal] });
  expect(shared.colorPool).toBe(room.colorPool);
  expect(shared.colorPool.byResource.get(first.resource)).toBe(0);
});

test('the color pool rotates when full and only frees a color after its last resource leaves', () => {
  const rows = Array.from({ length: 14 }, (_, i) => demand(i + 1, [], { kind: 'queue', resource: `video-${i}`, position: i + 1 }));
  const event: BlockEvent = { at: 1000, running: true, reset: true, capacity: 4096, used: 0, active: 0, open: 1,
    prepareAheadMS: 500000, downloadGraceMS: 30000, blocks: [], removed: [], demands: rows.slice(0, 9).reverse(), removedDemands: [] };
  const initial = applyBlockEvent(undefined, event);
  const delta = { ...event, reset: false, demands: [] };
  const color = (snapshot: typeof initial, i: number) => snapshot.colorPool.byResource.get(rows[i].resource);
  expect(rows.slice(0, 9).map((_, i) => color(initial, i))).toEqual([0, 1, 2, 3, 4, 5, 6, 7, 8]);

  const full = applyBlockEvent(initial, { ...delta, demands: rows.slice(9, 11) });
  expect([color(full, 9), color(full, 10)]).toEqual([0, 1]);
  const partial = applyBlockEvent(full, { ...delta, removedDemands: [rows[0].id], demands: [rows[11]] });
  expect(color(partial, 9)).toBe(0);
  expect(color(partial, 11)).toBe(2);

  const olderRelease = applyBlockEvent(partial, { ...delta, removedDemands: [rows[9].id] });
  const newerRelease = applyBlockEvent(olderRelease, { ...delta, removedDemands: [rows[4].id] });
  const refilled = applyBlockEvent(newerRelease, { ...delta, demands: rows.slice(12) });
  expect([color(refilled, 12), color(refilled, 13)]).toEqual([0, 4]);
  expect(color(refilled, 1)).toBe(1);
  expect(color(refilled, 8)).toBe(8);
  expect(color(initial, 0)).toBe(0);
});

test('offscreen timeline stops painting while demand events continue and unchanged rows do not repaint', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 500 });
  await page.addInitScript(() => {
    const metrics = { timeline: 0, demands: 0 };
    Object.assign(window, { observationPaints: metrics });
    const clear = CanvasRenderingContext2D.prototype.clearRect;
    CanvasRenderingContext2D.prototype.clearRect = function (...args) {
      if (this.canvas.classList.contains('timeline-canvas')) metrics.timeline++;
      if (this.canvas.classList.contains('demand-block-canvas')) metrics.demands++;
      return clear.apply(this, args);
    };
  });
  const f = await setup(page);
  const rows = [demand(1, f.blocks), demand(2, f.blocks, { kind: 'queue', position: 2 })];
  await f.emit({ demands: rows }); await f.open(); await f.canvas.scrollIntoViewIfNeeded();
  const paints = () => page.evaluate(() => ({ ...(window as unknown as { observationPaints: { timeline: number; demands: number } }).observationPaints }));
  await expect.poll(async () => (await paints()).timeline).toBeGreaterThan(2);
  const beforeCapacity = await paints();
  await f.emit({ active: 7, open: 17 });
  await expect(f.panel).toContainText('下载 7 / 16');
  expect((await paints()).demands).toBe(beforeCapacity.demands);
  await page.setViewportSize({ width: 1280, height: 200 });
  await f.panel.locator('.demand-block-canvas').first().evaluate(el => el.scrollIntoView({ block: 'start' }));
  await expect(f.canvas).not.toBeInViewport();
  // Edge contact still counts as intersecting; put the canvas fully offscreen.
  await expect.poll(() => f.canvas.evaluate(el => el.getBoundingClientRect().bottom)).toBeLessThan(0);
  // Let IntersectionObserver and React commit the visibility transition.
  const settle = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(resolve)))));
  await settle();
  const hidden = await paints();
  await expect.poll(f.totalStreams).toBe(1);
  const remaining = f.blocks.slice(0, -1);
  await f.emit({ removed: [f.blocks.at(-1)!.id], demands: rows.map(row => ({ ...row, blocks: remaining.map(b => b.id) })) });
  await expect(f.panel.locator('.demand-row-heading').first()).toContainText('47 块');
  await settle();
  expect((await paints()).timeline).toBe(hidden.timeline);
  expect((await paints()).demands).toBeGreaterThan(hidden.demands);
  await f.canvas.scrollIntoViewIfNeeded();
  await expect.poll(async () => (await paints()).timeline).toBeGreaterThan(hidden.timeline);
  await expect(f.canvas).toHaveAccessibleName(/47 个块/);
  await expect.poll(f.totalStreams).toBe(1);
});

test('formal playback redraws its canvas only when merged block membership changes', async ({ page }) => {
  await page.addInitScript(() => {
    const clear = CanvasRenderingContext2D.prototype.clearRect;
    let paints = 0;
    CanvasRenderingContext2D.prototype.clearRect = function (...args) {
      if (this.canvas.closest('[data-demand-id="1/fixture-resource"]')) document.body.dataset.formalPaints = String(++paints);
      return clear.apply(this, args);
    };
  });
  const f = await setup(page, 256);
  const formal = demand(1, [], {
    kind: 'formal', totalBlocks: 256, playback: { zeroAt: f.at - 20000, endAt: f.at + 160000 },
  });
  const read = demand(2, f.blocks);
  const queueBlock = block(0, f.at, { id: 'queue/0', resource: 'queue', songID: '91', totalBlocks: 1 });
  const queue = demand(3, [queueBlock], { kind: 'queue', position: 2, resource: 'queue', songID: '91' });
  await f.emit({ blocks: [queueBlock], demands: [formal, read, queue] });
  const paints = async () => Number(await page.locator('body').getAttribute('data-formal-paints'));
  await expect.poll(paints).toBeGreaterThan(0);
  const before = await paints();
  const queueRow = f.panel.locator(`[data-demand-id="${queue.id}"]`);
  for (let i = 0; i < 10; i++) {
    const position = i % 2 ? 2 : 3;
    await f.emit({ demands: [{ ...queue, position }] });
    await expect(queueRow).toContainText(`队列 #${position}`);
  }
  expect(await paints()).toBe(before);

  await f.emit({ demands: [{ ...read, blocks: read.blocks.slice(1) }] });
  await expect(f.panel.getByRole('group', { name: /正式播放.*游戏读取与交接 255 个块/ })).toBeVisible();
  expect(await paints()).toBeGreaterThan(before);
});

test('fixed blocks fit timeline lanes and labels follow Fluent caption typography without overlapping', async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  type AxisPaint = {
    labels: { text: string; x: number; y: number; width: number; font: string; ink: { left: number; right: number; top: number; bottom: number } }[];
    blocks: { y: number; height: number }[];
    pills: { x: number; y: number; width: number; height: number; opacity: number }[];
    dots: { y: number; radius: number }[];
    outlines: { y: number; height: number; stroke: number }[];
    bands: { x: number; y: number; height: number }[];
  };
  await page.addInitScript(() => {
    const paint: AxisPaint = { labels: [], blocks: [], pills: [], dots: [], outlines: [], bands: [] };
    Object.assign(window, { axisPaint: paint });
    const clear = CanvasRenderingContext2D.prototype.clearRect, text = CanvasRenderingContext2D.prototype.fillText, rounded = CanvasRenderingContext2D.prototype.roundRect, arc = CanvasRenderingContext2D.prototype.arc;
    CanvasRenderingContext2D.prototype.clearRect = function (...args) {
      if (this.canvas.classList.contains('timeline-canvas')) for (const entries of Object.values(paint)) entries.length = 0;
      return clear.apply(this, args);
    };
    const fillRect = CanvasRenderingContext2D.prototype.fillRect;
    CanvasRenderingContext2D.prototype.fillRect = function (x, y, width, height) {
      if (this.canvas.classList.contains('timeline-canvas')) {
        const style = getComputedStyle(this.canvas);
        if (['--timeline-amber-wash', '--timeline-blue-wash'].some(token => style.getPropertyValue(token).trim() === this.fillStyle)) {
          paint.bands.push({ x, y, height });
        }
      }
      return fillRect.call(this, x, y, width, height);
    };
    CanvasRenderingContext2D.prototype.arc = function (x, y, radius, start, end, counterclockwise) {
      if (this.canvas.classList.contains('timeline-canvas')) paint.dots.push({ y, radius });
      return arc.call(this, x, y, radius, start, end, counterclockwise);
    };
    CanvasRenderingContext2D.prototype.fillText = function (value, x, y, maxWidth) {
      if (this.canvas.classList.contains('timeline-canvas')) {
        const m = this.measureText(value);
        paint.labels.push({ text: value, x, y, width: m.width, font: this.font, ink: { left: x - m.actualBoundingBoxLeft, right: x + m.actualBoundingBoxRight, top: y - m.actualBoundingBoxAscent, bottom: y + m.actualBoundingBoxDescent } });
      }
      return text.call(this, value, x, y, maxWidth);
    };
    CanvasRenderingContext2D.prototype.roundRect = function (x, y, width, height, radii) {
      if (this.canvas.classList.contains('timeline-canvas')) {
        if (width === 16 && height === 16) paint.blocks.push({ y, height });
        if (width === height && width > 16) paint.outlines.push({ y, height, stroke: this.lineWidth });
        if (width > height) paint.pills.push({ x, y, width, height, opacity: this.globalAlpha });
      }
      return rounded.call(this, x, y, width, height, radii);
    };
  });
  const readPaint = () => page.evaluate(() => (window as unknown as { axisPaint: AxisPaint }).axisPaint);
  const lanePills = (paint: AxisPaint) => paint.pills.filter(pill => pill.opacity < 1);
  const readLaneLabels = (paint: AxisPaint) => paint.labels.filter(label => ['待下载', '下载中', '本地'].includes(label.text));
  const f = await setup(page, 0);
  // Use different Fluent body/caption fonts to catch hard-coded Canvas typography.
  await page.addStyleTag({ content: '.app-provider { --fontSizeBase300: 16px; --lineHeightBase300: 22px; --fontSizeBase200: 13px; --lineHeightBase200: 18px; --fontFamilyBase: Arial, sans-serif; }' });
  const bodySize = await f.panel.locator('.block-demand-cache').evaluate(el => parseFloat(getComputedStyle(el).fontSize));
  const caption = await f.panel.locator('.block-demand-capacity').evaluate(el => { const style = getComputedStyle(el); return { fontSize: style.fontSize, fontFamily: style.fontFamily, weight: style.getPropertyValue('--fontWeightSemibold').trim() }; });
  expect(parseFloat(caption.fontSize)).toBeLessThan(bodySize);
  const timed = ['waiting', 'downloading', 'available'].map((state, i) => block(i, f.at, { state: state as BlockView['state'], readers: 1 }));
  await f.detail(timed[0]);
  await f.emit({ reset: true, blocks: timed, used: 3 }); await f.open();
  expect(await f.panel.locator('.timeline-legend').evaluate(el => getComputedStyle(el).fontSize)).toBe(caption.fontSize);
  const layered = f.panel.getByRole('button', { name: '分层显示' });
  await expect(f.panel.locator('#block-timeline-content').getByRole('slider')).toHaveCount(0);
  await expect(f.panel.locator('.timeline-help')).toHaveCount(0);
  const windowBounds = await f.panel.locator('.timeline-window-key').first().boundingBox(), layerButtonBounds = await layered.boundingBox();
  const title = f.panel.getByText('时间轴视图', { exact: true });
  await expect(title).toBeVisible();
  const titleBounds = (await title.boundingBox())!;
  expect(titleBounds.x + titleBounds.width).toBeLessThan(windowBounds!.x);
  expect(titleBounds.y).toBeLessThan(windowBounds!.y + windowBounds!.height);
  expect(titleBounds.y + titleBounds.height).toBeGreaterThan(windowBounds!.y);
  expect(layerButtonBounds!.y).toBeLessThan(windowBounds!.y + windowBounds!.height);
  expect(layerButtonBounds!.y + layerButtonBounds!.height).toBeGreaterThan(windowBounds!.y);
  for (const untimed of [false, true]) {
    await f.emit({ reset: true, blocks: untimed ? [...timed, ...timed.map((b, i) => ({ ...b, id: `untimed/${i}`, at: 0, needBy: 0, timeBasis: 'untimed' as const }))] : timed, used: untimed ? 6 : 3 });
    if (untimed) await f.panel.getByRole('button', { name: '放大时间轴' }).click();
    let singleBands: number[] = [];
    for (const layers of [false, true]) {
      if (await layered.getAttribute('aria-pressed') !== String(layers)) await layered.click();
      await f.canvas.scrollIntoViewIfNeeded();
      if (layers) {
        await f.canvas.press('Home');
        await expect.poll(async () => (await readPaint()).outlines.some(outline => outline.height === 22)).toBe(true);
      }
      const paint = await readPaint();
      const plotStart = timelineRange(await f.canvas.evaluate(canvas => canvas.clientWidth))[0];
      expect(paint.bands).toHaveLength(2);
      for (const band of paint.bands) {
        expect(band.y).toBe(0);
        expect(band.height).toBe(timelineLayout(layers, true, untimed, 16).axis);
      }
      expect(paint.bands[0].x).toBeCloseTo(plotStart);
      if (layers) {
        for (const [index, band] of paint.bands.entries()) expect(band.x).toBeCloseTo(singleBands[index]);
      } else {
        singleBands = paint.bands.map(band => band.x);
        expect(lanePills(paint)).toHaveLength(0);
      }
      const height = await f.canvas.evaluate(c => c.clientHeight), now = paint.labels.find(label => label.text.startsWith('现在 '))!;
      expect(now).toBeTruthy(); expect(now.y).toBeLessThan(height - 6);
      const pill = paint.pills[0], gaps = { left: now.ink.left - pill.x, right: pill.x + pill.width - now.ink.right, top: now.ink.top - pill.y, bottom: pill.y + pill.height - now.ink.bottom };
      await info.attach('current-time-label', { body: JSON.stringify({ pill, text: now, gaps }), contentType: 'application/json' });
      expect.soft(Math.abs(gaps.left - gaps.right), `Horizontal centering: ${JSON.stringify(gaps)}`).toBeLessThanOrEqual(1);
      expect.soft(Math.abs(gaps.top - gaps.bottom), `Vertical centering: ${JSON.stringify(gaps)}`).toBeLessThanOrEqual(1);
      expect.soft(Math.max(gaps.left, gaps.right), 'Compact horizontal padding').toBeLessThanOrEqual(8);
      expect.soft(Math.max(gaps.top, gaps.bottom), 'Compact vertical padding').toBeLessThanOrEqual(5);
      expect(paint.labels.some(label => label.text.startsWith('块 '))).toBe(false);
      for (const label of paint.labels) expect(label.font).toContain(`${caption.fontSize} ${caption.fontFamily}`);
      expect(now.font).toBe(`${caption.weight} ${caption.fontSize} ${caption.fontFamily}`);
      expect(paint.blocks.length).toBe(untimed ? 6 : 3);
      for (const b of paint.blocks) { expect(b.height).toBe(16); expect(b.y - 12).toBeGreaterThanOrEqual(0); expect(b.y + b.height + 4).toBeLessThan(height); }
      const timedBlocks = paint.blocks.slice(0, 3);
      const topDotEdge = Math.min(...paint.dots.map(dot => dot.y - dot.radius));
      expect(topDotEdge - timelineLayout(layers, true, untimed, 16).plotTop).toBe(2.5);
      expect(now.y).toBeGreaterThan(Math.max(...timedBlocks.map(b => b.y + b.height)));
      if (layers) {
        const laneBlocks = [...timedBlocks].sort((a, b) => a.y - b.y);
        const laneLabels = readLaneLabels(paint).sort((a, b) => a.ink.top - b.ink.top);
        const dots = paint.dots.filter(dot => dot.y < timelineLayout(true, true, false, 16).axis).sort((a, b) => a.y - b.y);
        expect(dots).toHaveLength(laneBlocks.length);
        expect(laneLabels).toHaveLength(3);
        const overlays = lanePills(paint);
        expect(overlays).toHaveLength(3);
        for (const [index, block] of laneBlocks.entries()) {
          const label = laneLabels[index];
          const overlay = overlays[index];
          expect(overlay.x + overlay.width).toBeLessThan(paint.bands[0].x);
          expect(overlay.x + overlay.width).toBeGreaterThan(label.ink.right);
          expect(overlay.opacity).toBeGreaterThan(0);
          expect(label.x).toBe(12);
          expect((label.ink.top + label.ink.bottom) / 2).toBeCloseTo(block.y + 8);
          if (index > 0) {
            const previous = laneBlocks[index - 1];
            expect(block.y - previous.y).toBe(34);
            const previousBottom = Math.max(previous.y + 16.75, ...paint.outlines.filter(outline => outline.y >= previous.y - 4 && outline.y <= previous.y).map(outline => outline.y + outline.height + outline.stroke / 2));
            const gap = dots[index].y - dots[index].radius - previousBottom;
            expect(gap).toBeGreaterThanOrEqual(2.5);
            if (index === 1) expect(gap).toBe(2.5);
          }
        }
      }
      if (untimed) {
        const untimedBlocks = paint.blocks.slice(3), labels = paint.labels.filter(label => label.text.startsWith('无期限'));
        expect(new Set(untimedBlocks.map(b => b.y)).size).toBe(1);
        expect(untimedBlocks[0].y).toBe(timelineLayout(layers, true, true, 16).untimedY);
        expect(labels).toHaveLength(1);
        expect(labels[0].text).toBe('无期限 · 3 块');
        expect(labels[0].ink.bottom).toBeLessThan(Math.min(...paint.dots.filter(dot => dot.y > now.y).map(dot => dot.y - dot.radius)));
        expect(height - untimedBlocks[0].y - untimedBlocks[0].height).toBe(12);
      }
      const ticks = paint.labels.filter(label => /^\d{2}:\d{2}/.test(label.text));
      for (const [index, tick] of ticks.entries()) {
        expect(tick.y).toBe(now.y);
        expect(Math.abs(tick.x - now.x)).toBeGreaterThan((tick.width + now.width) / 2);
        if (index > 0) expect(tick.x - tick.width / 2 - (ticks[index - 1].x + ticks[index - 1].width / 2)).toBeGreaterThanOrEqual(8);
      }
      if (layers) { await f.canvas.press('Escape'); await expect(page.getByRole('group', { name: '块需求详情' })).toBeHidden(); }
      await f.panel.screenshot({ path: info.outputPath(`axis-${layers ? 'layered' : 'single'}-${untimed ? 'mixed' : 'timed'}.png`) });
    }
  }
  await f.canvas.dispatchEvent('wheel', { deltaX: 20 });
  await expect.poll(async () => (await readPaint()).bands[0].x).toBeCloseTo(40, 0);
  const crossing = await readPaint();
  expect(readLaneLabels(crossing).map(label => [label.text, label.x])).toEqual([['待下载', 12], ['下载中', 12], ['本地', 12]]);
  expect(lanePills(crossing)).toHaveLength(3);
  for (const pill of lanePills(crossing)) expect(pill.x + pill.width).toBeLessThan(crossing.bands[0].x);

  await f.canvas.dispatchEvent('wheel', { deltaX: 35 });
  await expect.poll(async () => lanePills(await readPaint())).toHaveLength(0);
  expect(readLaneLabels(await readPaint()).map(label => [label.text, label.x])).toEqual([['待下载', 12], ['下载中', 12], ['本地', 12]]);
  await f.panel.getByRole('button', { name: '跟随现在', pressed: false }).click();
  await expect.poll(async () => lanePills(await readPaint())).toHaveLength(3);

  await layered.click();
  const older = block(9, f.at - 45000, { state: 'available' });
  await f.detail(older);
  await f.emit({ reset: true, blocks: [older, ...timed], used: 4 });
  await expect(f.canvas).toHaveAccessibleName(/4 个块/);
  const olderX = await f.xAt(older.at);
  expect(olderX).toBeLessThan((await readPaint()).bands[0].x);
  await f.canvas.click({ position: { x: olderX, y: singleY } });
  const popup = page.getByRole('group', { name: '块需求详情' });
  await expect(popup).toContainText('块 10/');
  await f.canvas.press('Escape'); await f.canvas.press('Home');
  await expect(popup).toContainText('块 10/');
  await expect(f.viewport).toHaveAttribute('data-follow', 'true');
});

test('empty and untimed-only states stay compact and restore timed controls with focus intact', async ({ page }, info) => {
  const f = await setup(page, 1), layered = f.panel.getByRole('button', { name: '分层显示' });
  await f.open(); await layered.click();
  await layered.focus();
  await f.emit({ reset: true, blocks: [], demands: [], used: 0, active: 0 });
  await expect(f.canvas).toBeHidden();
  await expect(f.panel.locator('.timeline-controls')).toBeHidden();
  await expect(f.panel.locator('.timeline-legend')).toBeHidden();
  await expect(f.panel.locator('.demand-view-heading')).toBeHidden();
  await expect(f.panel.locator('.demand-empty')).toHaveCount(1);
  await expect(f.panel.locator('.demand-empty')).toBeVisible();
  await expect(f.viewport).toBeFocused();
  const toggle = f.panel.getByRole('button', { name: '收起时间轴视图', exact: true });
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect.poll(f.totalStreams).toBe(1);
  await f.panel.screenshot({ path: info.outputPath('empty-compact.png') });

  const untimed = block(1, 0, { state: 'available', readers: 1 });
  await f.detail(untimed);
  await f.emit({ blocks: [untimed], demands: [demand(1, [untimed], { kind: 'batch' })], used: 1 });
  await expect(f.canvas).toHaveAccessibleName(/1 个块，单层/);
  await expect(f.canvas).toBeVisible();
  await expect(f.panel.locator('.timeline-legend')).toBeVisible();
  await expect(f.panel.locator('.timeline-window-key.ahead')).toBeHidden();
  await expect(f.panel.locator('.timeline-controls')).toBeHidden();
  expect(await f.canvas.evaluate(c => c.clientHeight)).toBeLessThan(80);
  await f.panel.screenshot({ path: info.outputPath('only-untimed-compact.png') });
  await f.canvas.press('Home');
  await expect(page.getByRole('group', { name: '块需求详情' })).toContainText('无有限期限');
  await f.canvas.press('Escape');

  await f.emit({ blocks: [block(2, f.at)] });
  await expect(f.panel.locator('.timeline-controls')).toBeVisible();
  await expect(layered).toHaveAttribute('aria-pressed', 'true');
  await expect(f.canvas).toHaveAccessibleName(/2 个块，三层/);
  await toggle.focus();
  await f.emit({ removed: ['resource/2'] });
  await expect(f.panel.locator('.timeline-controls')).toBeHidden();
  await expect(toggle).toBeFocused();
  await f.canvas.focus();
  await f.emit({ reset: true, blocks: [], demands: [], used: 0 });
  await expect(f.canvas).toBeHidden(); await expect(f.viewport).toBeFocused();
});

for (const layers of [false, true]) test(`untimed dragging moves its row without changing the timed view when layers are ${layers}`, async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page, 0), untimed = Array.from({ length: 200 }, (_, i) => block(i, 0, { state: 'available', totalBlocks: 200 }));
  for (const b of [untimed[0], untimed[10]]) await f.detail(b);
  await f.emit({ reset: true, blocks: [block(1000, f.at), ...untimed], used: 201 }); await f.open();
  if (layers) await f.panel.getByRole('button', { name: '分层显示' }).click();
  const rect = (await f.canvas.boundingBox())!, y = timelineLayout(layers, true, true, 16).untimedY + 8;
  const drag = async (atY: number, dx: number) => {
    await page.mouse.move(rect.x + rect.width / 2, rect.y + atY); await page.mouse.down();
    await page.mouse.move(rect.x + rect.width / 2 + dx, rect.y + atY, { steps: 8 }); await page.mouse.up();
  };
  const before = await f.viewport.getAttribute('data-center'), span = await f.viewport.getAttribute('data-span');
  await drag(y, -80);
  await expect(f.viewport).toHaveAttribute('data-center', before!); await expect(f.viewport).toHaveAttribute('data-follow', 'true');
  await expect(f.viewport).toHaveAttribute('data-span', span!);
  const popup = page.getByRole('group', { name: '块需求详情' });
  await f.canvas.click({ position: { x: 36, y } }); await expect(popup).toContainText('块 11/200');
  await popup.getByRole('button', { name: '关闭块详情' }).click();

  await drag(singleY, -80);
  await expect(f.viewport).toHaveAttribute('data-follow', 'false');
  expect(await f.viewport.getAttribute('data-center')).not.toBe(before);
  await f.canvas.click({ position: { x: 36, y } }); await expect(popup).toContainText('块 11/200');
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  const fixedCenter = await f.viewport.getAttribute('data-center');
  await drag(y, 200);
  await expect(f.viewport).toHaveAttribute('data-center', fixedCenter!); await expect(f.viewport).toHaveAttribute('data-follow', 'false');
  await f.canvas.click({ position: { x: 36, y } }); await expect(popup).toContainText('块 1/200');
});

test('blank space before dense untimed blocks never opens an overlap or detail stream', async ({ page }) => {
  const f = await setup(page, 0), blocks = Array.from({ length: 200 }, (_, i) => block(i, 0, { state: 'available', totalBlocks: 200 }));
  await page.addStyleTag({ content: '#block-demands { width: 800px; }' });
  await f.emit({ reset: true, blocks, used: 200 }); await f.open();
  expect(await f.canvas.evaluate(c => c.clientWidth)).toBe(800);
  const y = await f.canvas.evaluate(c => c.clientHeight - 20);
  for (const x of [12, 16, 23]) {
    await f.canvas.click({ position: { x, y } });
    await page.evaluate(() => new Promise(requestAnimationFrame));
    await expect(page.getByRole('group', { name: '块需求详情' })).toBeHidden();
  }
  await expect.poll(f.totalStreams).toBe(1);
});

for (const layers of [false, true]) test(`untimed blocks stay in one row with horizontal wheel and keyboard when layers are ${layers}`, async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const f = await setup(page, 1), blocks = Array.from({ length: 200 }, (_, i) => block(i, 0, { state: (['waiting', 'downloading', 'available'] as const)[i % 3], totalBlocks: 200 }));
  for (const b of [...blocks.slice(0, 3), blocks[199]]) await f.detail(b);
  await f.open();
  if (layers) await f.panel.getByRole('button', { name: '分层显示' }).click();
  await f.emit({ reset: true, blocks, used: 200 });
  await expect(f.panel.locator('.timeline-controls')).toBeHidden();
  const before = await f.viewport.getAttribute('data-center'), rect = (await f.canvas.boundingBox())!, y = timelineLayout(layers, false, true, 16).untimedY + 8;
  const popup = page.getByRole('group', { name: '块需求详情' });
  for (let i = 0; i < 3; i++) {
    await f.canvas.click({ position: { x: 24 + i * 8 + 12, y } });
    await expect(popup).toContainText(`块 ${i + 1}/200`);
    await expect(popup).toContainText('重叠 2 块');
    await expect(popup).toContainText('无有限期限');
    await popup.getByRole('button', { name: '关闭块详情' }).click();
  }
  await page.mouse.move(rect.x + rect.width / 2, rect.y + y); await page.mouse.wheel(10000, 0);
  await f.canvas.click({ position: { x: rect.width - 28, y } });
  await expect(popup).toContainText('块 200/200');
  expect(await f.viewport.getAttribute('data-center')).toBe(before);
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  const timed = block(200, f.at);
  await f.emit({ blocks: [timed] });
  await expect(f.panel.locator('.timeline-controls')).toBeVisible();
  await f.panel.getByRole('button', { name: '分层显示' }).click();
  await f.emit({ removed: [timed.id] });
  await expect(f.panel.locator('.timeline-controls')).toBeHidden();
  await f.canvas.click({ position: { x: rect.width - 28, y: timelineLayout(!layers, false, true, 16).untimedY + 8 } });
  await expect(popup).toContainText('块 200/200');
  await popup.getByRole('button', { name: '关闭块详情' }).click();
  await f.canvas.press('Home'); await expect(popup).toContainText('块 1/200');
  await f.canvas.press('End'); await expect(popup).toContainText('块 200/200');
  await expect(f.panel.getByRole('slider')).toHaveCount(0);
});

test('untimed spacing adapts to width and count, caps overlap and releases overflow without reserving slider height', async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 1000 });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const readBlocks = await observeTimelineBlocks(page);
  const f = await setup(page, 0);
  const readPaint = async () => (await readBlocks()).filter(block => block.width === 16 && block.height === 16).sort((a, b) => a.x - b.x);
  const emit = async (count: number) => {
    const blocks = Array.from({ length: count }, (_, i) => block(i, 0, { state: 'available', totalBlocks: count }));
    await f.emit({ reset: true, blocks, used: count });
    await expect(f.canvas).toHaveAttribute('aria-label', new RegExp(`${count} 个块`));
    return blocks;
  };
  await emit(12); await f.open();
  const canvasHeight = await f.canvas.evaluate(c => c.clientHeight);
  await expect.poll(async () => (await readPaint()).length).toBe(12);
  let paint = await readPaint();
  expect(paint[0].x).toBe(24); expect(paint[1].x - paint[0].x).toBe(22);
  await expect(f.panel.getByRole('slider')).toHaveCount(0);
  await f.panel.screenshot({ path: info.outputPath('untimed-spaced.png') });

  const available = await f.canvas.evaluate(c => c.clientWidth - 48), packedCount = Math.ceil((available - 16) / 12) + 1;
  const packed = await emit(packedCount);
  await f.detail(packed[0]); await f.detail(packed[1]);
  await expect.poll(async () => (await readPaint()).length).toBe(packedCount);
  paint = await readPaint();
  const packedPitch = paint[1].x - paint[0].x;
  expect(packedPitch).toBeGreaterThan(8); expect(packedPitch).toBeLessThan(16);
  expect(paint.at(-1)!.x + 16).toBeCloseTo(available + 24);
  expect(new Set(paint.map(b => b.y)).size).toBe(1);
  const popup = page.getByRole('group', { name: '块需求详情' });
  await f.canvas.click({ position: { x: 24 + packedPitch + 1, y: paint[0].y + 8 } });
  await expect(popup).toContainText(`块 1/${packedCount}`); await expect(popup).toContainText('重叠 2 块');
  await popup.getByRole('button', { name: '下一个重叠块' }).click(); await expect(popup).toContainText(`块 2/${packedCount}`);
  await popup.getByRole('button', { name: '关闭块详情' }).click(); await page.mouse.move(0, 0);
  await f.panel.screenshot({ path: info.outputPath('untimed-packed.png') });

  await page.setViewportSize({ width: 1920, height: 1000 });
  await expect.poll(async () => { const p = await readPaint(); return p[1].x - p[0].x; }).toBeGreaterThan(packedPitch);
  paint = await readPaint(); expect(paint[1].x - paint[0].x).toBeLessThanOrEqual(22);
  expect(await f.canvas.evaluate(c => c.clientHeight)).toBe(canvasHeight);

  const overflowCount = Math.ceil(await f.canvas.evaluate(c => c.clientWidth) / 8) + 1;
  const overflowing = await emit(overflowCount); await f.detail(overflowing[overflowCount - 1]);
  await expect.poll(async () => { const p = await readPaint(); return p[1].x - p[0].x; }).toBe(8);
  expect((await readPaint()).length).toBeLessThan(overflowCount);
  await expect(f.panel.getByRole('slider')).toHaveCount(0);
  await f.panel.screenshot({ path: info.outputPath('untimed-overflow.png') });
  await f.canvas.press('End'); await expect(popup).toContainText(`块 ${overflowCount}/${overflowCount}`);
  await popup.getByRole('button', { name: '关闭块详情' }).click(); await page.mouse.move(0, 0);

  await emit(12);
  await expect.poll(async () => (await readPaint()).length).toBe(12);
  paint = await readPaint(); expect(paint[0].x).toBe(24); expect(paint[1].x - paint[0].x).toBe(22);
  expect(await f.canvas.evaluate(c => c.clientHeight)).toBe(canvasHeight);
  const gap = await f.panel.evaluate(panel => {
    const canvas = panel.querySelector('.timeline-canvas')!.getBoundingClientRect(), list = panel.querySelector('.block-demand-view')!.getBoundingClientRect();
    return list.top - canvas.bottom;
  });
  expect(gap).toBe(0);
  await expect(f.panel.getByRole('slider')).toHaveCount(0);
});

test('timeline shares cached block images and refreshes them for state and theme changes', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'light' });
  const readPaint = await observeTimelineBlocks(page);
  const f = await setup(page, 0);
  const first = block(0, f.at + 10000, { state: 'available' });
  const second = block(1, f.at + 100000, { state: 'available' });
  const images = async () => (await readPaint()).flatMap(block => block.imageID ? [block.imageID] : []);
  await f.emit({ blocks: [first, second] });
  await f.open();
  await expect.poll(async () => {
    const drawn = await images();
    return drawn.length === 2 && drawn[0] === drawn[1];
  }).toBe(true);

  await f.emit({ blocks: [{ ...second, state: 'downloading' }] });
  await expect.poll(async () => new Set(await images()).size).toBe(2);
  const light = await images();

  const rows = f.panel.locator('.demand-row');
  const height = () => f.viewport.evaluate(el => el.clientHeight);
  const originalHeight = await height();
  const reading = demand(1, [first, second]);
  await f.emit({ demands: [reading] });
  await expect(rows).toHaveCount(1);
  await expect.poll(height).toBeGreaterThan(originalHeight);
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(await images()).toEqual(light);

  await f.emit({ removedDemands: [reading.id] });
  await expect(rows).toHaveCount(0);
  await expect.poll(height).toBe(originalHeight);
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(await images()).toEqual(light);

  await page.emulateMedia({ colorScheme: 'dark' });
  await expect.poll(async () => (await images()).filter(id => !light.includes(id)).length).toBe(2);
});

test('4096 blocks render with viewport canvases and no animation after collapse', async ({ page }, info) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.addInitScript(() => {
    const metrics = { paints: 0, durations: [] as number[] }; Object.assign(window, { timelineMetrics: metrics });
    const clear = CanvasRenderingContext2D.prototype.clearRect;
    CanvasRenderingContext2D.prototype.clearRect = function (...args) { if (this.canvas.classList.contains('timeline-canvas')) metrics.paints++; return clear.apply(this, args); };
    const request = window.requestAnimationFrame;
    window.requestAnimationFrame = callback => request.call(window, time => { const paints = metrics.paints, start = performance.now(); callback(time); if (metrics.paints > paints) metrics.durations.push(performance.now() - start); });
  });
  const f = await setup(page, 4096); await f.emit({ demands: [demand(1, f.blocks)] }); await f.open();
  await expect(f.canvas).toHaveAccessibleName(/4096 个块/); await expect(f.panel.locator('canvas')).toHaveCount(2);
  await expect.poll(() => page.evaluate(() => (window as unknown as { timelineMetrics: { paints: number } }).timelineMetrics.paints)).toBeGreaterThan(20);
  await f.panel.screenshot({ path: info.outputPath('timeline-4096.png') });
  const metrics = await page.evaluate(() => (window as unknown as { timelineMetrics: { paints: number; durations: number[] } }).timelineMetrics);
  const metricsPath = info.outputPath('canvas-frame-cost.json');
  await writeFile(metricsPath, JSON.stringify(metrics));
  await info.attach('canvas-frame-cost', { path: metricsPath, contentType: 'application/json' });
  await f.panel.getByRole('button', { name: '收起时间轴视图' }).click(); await expect.poll(f.totalStreams).toBe(1);
  const paints = await page.evaluate(() => (window as unknown as { timelineMetrics: { paints: number } }).timelineMetrics.paints);
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  expect(await page.evaluate(() => (window as unknown as { timelineMetrics: { paints: number } }).timelineMetrics.paints)).toBe(paints);
});
