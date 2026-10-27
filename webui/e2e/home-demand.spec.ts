import { test, expect, type Page } from '@playwright/test';
import { mockAPI } from './api';

async function setPageHidden(page: Page, hidden: boolean) {
  await page.evaluate(value => { Object.defineProperty(document, 'hidden', { configurable: true, value }); document.dispatchEvent(new Event('visibilitychange')); }, hidden);
}

async function setup(page: Page) {
  const api = await mockAPI(page), at = Date.now();
  api.state.running = true; api.state.hosts.ready = true;
  const blocks = ['available', 'waiting', 'downloading', 'protected'].map((state, i) => ({
    id: `video/${i}`, resource: 'video', songID: '42', title: '夏日的风', block: i, byteOffset: i * 1048576, totalBlocks: 4, bytes: 1048576,
    at: at + i * 10000, needBy: at + i * 10000, deadline: at + i * 10000 + 30000, timeBasis: 'need-by',
    protectionStart: 0, protectionUntil: i === 3 ? at + 60000 : 0, protected: i === 3, inUse: i !== 3, sources: i === 1 ? 2 : 1, readers: i === 1 ? 1 : 0, state, windowClosed: false, opportunistic: false, eligible: true,
  }));
  const demands = [
    { id: '1/video', sourceID: 1, kind: 'get', songID: '42', title: '夏日的风', position: 0, resource: 'video', totalBlocks: 4, blocks: ['video/0', 'video/1'] },
    { id: '2/video', sourceID: 2, kind: 'queue', songID: '42', title: '夏日的风', position: 1, resource: 'video', totalBlocks: 4, blocks: ['video/1', 'video/2'] },
  ];
  const emit = async (kind: string, state: unknown) => page.request.post('/__fixture/events', { data: { id: api.fixtureID, kind, state } });
  await emit('blocks', { at, running: true, reset: true, capacity: 4096, used: 3, active: 1, open: 1, prepareAheadMS: 500000, downloadGraceMS: 30000, blocks, demands, removed: [], removedDemands: [] });
  await page.addInitScript(() => {
    const paints = { axis: 0, rows: 0 }; Object.assign(window, { demandPaints: paints });
    const clear = CanvasRenderingContext2D.prototype.clearRect;
    CanvasRenderingContext2D.prototype.clearRect = function (...args) {
      if (this.canvas.classList.contains('timeline-canvas')) paints.axis++;
      if (this.canvas.classList.contains('demand-block-canvas')) paints.rows++;
      return clear.apply(this, args);
    };
  });
  await page.goto('/#/home');
  const panel = page.locator('#block-demands'), speed = page.getByLabel('总下载速度', { exact: true });
  await expect(panel.locator('.demand-row')).toHaveCount(2);
  const streams = async () => (await (await page.request.get('/__fixture/streams?id=' + api.fixtureID)).json()).blocks || 0;
  return { ...api, panel, speed, emit, streams };
}

test('home uses live demands and unique block counts without download polling or axis animation', async ({ page }) => {
  const f = await setup(page);
  await expect(f.panel.getByRole('heading', { name: '视频块需求', exact: true })).toBeVisible();
  await expect(f.panel.getByRole('checkbox')).toHaveCount(0);
  await expect(f.panel.getByText('GET → 短期交接', { exact: false })).toHaveCount(0);
  await expect(f.panel.getByRole('button', { name: '展开时间轴视图' })).toHaveAttribute('aria-expanded', 'false');
  await expect(f.panel.locator('.timeline-canvas')).toBeHidden();
  for (const label of ['等待下载', '正在下载', '本地可用', '延续保护']) await expect(f.panel.locator('.timeline-legend')).toContainText(label + ' 1');
  await expect(f.panel.locator('.demand-progress').first()).toHaveText('50%');
  await expect(f.panel.locator('.demand-progress').nth(1)).toHaveText('0%');
  await expect(f.panel.getByRole('progressbar')).toHaveCount(0);
  await expect.poll(f.streams).toBe(1);
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const paints = () => page.evaluate(() => ({ ...(window as unknown as { demandPaints: { axis: number; rows: number } }).demandPaints }));
  const before = await paints(); expect(before.axis).toBe(0); expect(before.rows).toBeGreaterThan(0);
  await f.emit('download-speed', { bytesPerSecond: 2000000 }); await expect(f.speed).toHaveText('2.000 MB/s');
  expect(await paints()).toEqual(before);
  await setPageHidden(page, true); await expect.poll(f.streams).toBe(0);
  await f.emit('download-speed', { bytesPerSecond: 9000000 }); await expect(f.speed).toHaveText('2.000 MB/s');
  await setPageHidden(page, false); await expect.poll(f.streams).toBe(1); await expect(f.speed).toHaveText('9.000 MB/s');
  await f.emit('download-speed', { bytesPerSecond: 0 }); await expect(f.speed).toHaveText('0.000 MB/s');
  await page.clock.install(); await page.clock.fastForward(12000);
  expect(f.calls.filter(call => call.path === 'downloads')).toEqual([]);
});

test('native reconnect resets a stale nonzero speed even when the server has no new speed event', async ({ page }) => {
  const f = await setup(page);
  await f.emit('download-speed', { bytesPerSecond: 2000000 }); await expect(f.speed).toHaveText('2.000 MB/s');
  await page.request.post('/__fixture/disconnect', { data: { id: f.fixtureID, kind: 'blocks' } });
  await expect(f.panel).toContainText('事件连接中断');
  await expect.poll(f.streams).toBe(0);
  await f.emit('download-speed', { bytesPerSecond: 0 });
  await expect(f.panel.getByText('事件已连接', { exact: true })).toBeVisible({ timeout: 10000 });
  await expect(f.speed).toHaveText('0.000 MB/s');
});

test('empty home keeps zero counts visible and legacy activity links reach the demand panel', async ({ page }) => {
  await mockAPI(page); await page.goto('/#downloads');
  const panel = page.locator('#block-demands');
  await expect(panel).toBeFocused();
  for (const label of ['等待下载', '正在下载', '本地可用', '延续保护']) await expect(panel.locator('.timeline-legend')).toContainText(label + ' 0');
  await expect(panel.locator('.timeline-canvas')).toBeHidden();
  await expect(panel).toContainText('当前没有使用需求');
});

test('timeline arrow has a tooltip and keyboard control and opens the axis above demands', async ({ page }, info) => {
  const f = await setup(page), toggle = f.panel.getByRole('button', { name: '展开时间轴视图', exact: true });
  await toggle.hover();
  await expect(page.getByRole('tooltip')).toHaveText('展开时间轴视图');
  await toggle.focus(); await page.keyboard.press('Enter');
  const collapse = f.panel.getByRole('button', { name: '收起时间轴视图', exact: true });
  await expect(collapse).toHaveAttribute('aria-expanded', 'true');
  await expect(f.panel.locator('.timeline-canvas')).toBeVisible();
  expect(await f.panel.evaluate(panel => panel.querySelector('#block-timeline-content')!.compareDocumentPosition(panel.querySelector('.block-demand-view')!) & Node.DOCUMENT_POSITION_FOLLOWING)).toBeTruthy();
  await f.panel.focus(); await page.mouse.move(0, 0);
  await expect(page.getByRole('tooltip')).toBeHidden();
  await f.panel.screenshot({ path: info.outputPath('compact-timeline-and-demands.png') });
  await collapse.focus(); await page.keyboard.press('Space');
  await expect(f.panel.locator('.timeline-canvas')).toBeHidden();
  await expect.poll(f.streams).toBe(1);
});

test('compact demand rows keep the full canvas visible at minimum and maximum block sizes', async ({ page }) => {
  const f = await setup(page), slider = f.panel.getByRole('slider', { name: '块显示大小', exact: true });
  for (const key of ['Home', 'End']) {
    await slider.focus(); await slider.press(key);
    const rows = await f.panel.locator('.demand-row').evaluateAll(rows => rows.map(row => ({ bottom: row.getBoundingClientRect().bottom, canvasBottom: row.querySelector('canvas')!.getBoundingClientRect().bottom })));
    for (const row of rows) expect(row.canvasBottom).toBeLessThan(row.bottom);
  }
});

for (const [width, height] of [[320, 900], [390, 900], [1280, 800], [1366, 768]]) test(`default demand view remains readable at ${width}px`, async ({ page }, info) => {
  await page.setViewportSize({ width, height }); await page.emulateMedia({ colorScheme: width === 390 ? 'dark' : 'light', forcedColors: width === 320 ? 'active' : 'none' });
  const f = await setup(page); await f.emit('download-speed', { bytesPerSecond: 1250000 });
  const eventStatus = f.panel.locator('.block-demand-heading').getByRole('status');
  await expect(eventStatus).toHaveText('事件已连接');
  await expect(eventStatus).toHaveAttribute('aria-atomic', 'true');
  await expect(eventStatus.locator('svg')).toHaveAttribute('aria-hidden', 'true');
  const statusStyle = (el: Element) => { const s = getComputedStyle(el); return [s.backgroundColor, s.color, s.fontSize, s.borderRadius, el.getBoundingClientRect().height]; };
  for (const name of ['后台程序', '游戏连接']) expect(await eventStatus.evaluate(statusStyle)).toEqual(await page.getByRole('status', { name, exact: true }).evaluate(statusStyle));
  await expect(f.panel.getByRole('slider', { name: '块显示大小', exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  if (width >= 1280) {
    const bounds = await f.panel.evaluate(panel => {
      const rect = (selector: string) => { const r = panel.querySelector(selector)!.getBoundingClientRect(); return { top: r.top, bottom: r.bottom }; };
      return { panelBottom: panel.getBoundingClientRect().bottom, title: rect('h2'), speed: rect('.block-demand-speed'), capacity: rect('.block-demand-capacity'), toggle: rect('button[aria-controls="block-timeline-content"]'), legend: rect('.timeline-legend'), size: rect('.timeline-size-control') };
    });
    expect(bounds.panelBottom).toBeLessThanOrEqual(height);
    for (const part of [bounds.speed, bounds.capacity, bounds.toggle]) { expect(part.top).toBeLessThan(bounds.title.bottom); expect(part.bottom).toBeGreaterThan(bounds.title.top); }
    expect(bounds.size.top).toBeLessThan(bounds.legend.bottom); expect(bounds.size.bottom).toBeGreaterThan(bounds.legend.top);
    await info.attach('layout', { body: JSON.stringify(bounds, null, 2), contentType: 'application/json' });
  }
  await page.screenshot({ path: info.outputPath('home-demand.png'), fullPage: true, animations: 'disabled' });
});
