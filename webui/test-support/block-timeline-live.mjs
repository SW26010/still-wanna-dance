// Invoked only by TestBlockTimelineBrowser with its isolated loopback service.
import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

const [url, evidence] = process.argv.slice(2);
if (new URL(url).hostname !== '127.0.0.1' || !evidence) throw new Error('isolated service and evidence directory required');
await mkdir(evidence, { recursive: true });
const browser = await chromium.launch({ channel: process.env.CI ? 'chromium' : 'msedge', headless: true });
const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
const page = await context.newPage(), errors = [];
page.on('pageerror', error => errors.push(error.message));
try {
  await page.goto(url + '/#/home');
  await page.getByRole('button', { name: '展开时间轴视图' }).click();
  const panel = page.locator('#block-demands'), canvas = panel.locator('.timeline-canvas');
  await expect(panel.getByText('事件已连接', { exact: true })).toBeVisible();
  const cache = panel.locator('.block-demand-cache');
  await expect(cache).toHaveText('块缓存 0.0 MiB');
  await expect(canvas).toHaveAccessibleName(/4 个块/);
  await expect(panel.locator('.demand-row')).toHaveCount(1);
  await expect(panel.locator('.demand-row')).toContainText('GET #');
  await expect(panel.locator('.demand-block-canvas')).toHaveAccessibleName(/视频共 4 块/);
  await expect.poll(() => panel.locator('.timeline-legend').innerText()).toMatch(/正在下载\s+[1-4]/);
  await canvas.focus(); await canvas.press('Home');
  const detail = page.getByRole('group', { name: '块需求详情' });
  await expect(detail).toContainText('播放请求 #');
  await expect(detail).toContainText('读取急需');
  await expect(detail).toContainText('need-by');
  await expect(detail).toContainText('块 1/4');
  await page.screenshot({ path: path.join(evidence, 'real-download.png'), fullPage: true });
  // Keep the pin across an actual offscreen interval. Neither the overview nor
  // detail stream may remain subscribed; resume observation before completion.
  await page.evaluate(() => window.scrollTo(0, 0));
  const offscreenHeight = await panel.evaluate(element => Math.max(1, Math.floor(element.getBoundingClientRect().top) - 1));
  await page.setViewportSize({ width: 1280, height: offscreenHeight });
  await expect.poll(() => panel.evaluate(element => element.getBoundingClientRect().top >= innerHeight)).toBe(true);
  await expect.poll(async () => (await page.request.get(url + '/__timeline/streams')).json()).toBe(0);
  await expect(detail).toHaveCount(0);
  await canvas.scrollIntoViewIfNeeded();
  await page.setViewportSize({ width: 1280, height: 900 });
  await expect(detail).toContainText('播放请求 #');
  await page.request.post(url + '/__timeline/release');
  await expect.poll(async () => parseFloat(await page.getByLabel('总下载速度', {exact:true}).innerText())).toBeGreaterThan(0);
  await expect(panel.locator('.timeline-legend')).toContainText('延续保护 4');
  await expect(cache).toHaveText('块缓存 3.0 MiB');
  expect(await (await page.request.get(url + '/__timeline/cache')).json()).toBe(3 * 1048576 + 37);
  await expect(panel).toContainText('需求占用 0 / 4096');
  await expect(panel.locator('.demand-row')).toHaveCount(0);
  await expect(detail).toContainText('已无使用需求，本地数据仍在延续保护期');
  await expect(detail).toContainText('沿用最后一次 need-by');
  await expect(detail).toContainText('块 1/4');
  await page.screenshot({ path: path.join(evidence, 'real-protection.png'), fullPage: true });
  await page.getByRole('button', { name: '关闭块详情' }).click();
  await page.getByRole('button', { name: '收起时间轴视图' }).click();
  await expect(canvas).toBeHidden();
  await expect.poll(async () => parseFloat(await page.getByLabel('总下载速度', {exact:true}).innerText())).toBe(0);
  await expect.poll(async () => (await page.request.get(url + '/__timeline/streams')).json()).toBe(1);
  await page.evaluate(() => { location.hash = '#/library'; });
  await expect.poll(async () => (await page.request.get(url + '/__timeline/streams')).json()).toBe(0);
  // Physical reclamation continues while the page has no observation worker.
  await expect.poll(async () => (await page.request.get(url + '/__timeline/cache')).json(), { timeout: 15000 }).toBe(0);
  await page.evaluate(() => { location.hash = '#/home'; });
  await expect(cache).toHaveText('块缓存 0.0 MiB');
  await page.screenshot({ path: path.join(evidence, 'real-cache-reclaimed.png'), fullPage: true });
  await page.evaluate(() => { location.hash = '#/library'; });
  await expect.poll(async () => (await page.request.get(url + '/__timeline/streams')).json()).toBe(0);
  expect(errors).toEqual([]);
  await writeFile(path.join(evidence, 'result.json'), JSON.stringify({ outcome: 'passed', browser: await browser.version(), cacheBytes: 3 * 1048576 + 37, flow: ['real HTTP Range download', 'SSE downloading and source details', 'zero SSE requests while offscreen', 'pinned details restored with four local protected blocks', 'cache display agrees with actual block files', 'speed event followed by zero', 'collapse keeps demand subscription', 'physical reclamation without subscribers', 'cache total refreshed to zero on return', 'zero SSE requests after navigation'], errors }, null, 2));
  console.log('Real console + scheduler + Range download + browser SSE lifecycle passed.');
} catch (error) {
  await page.screenshot({ path: path.join(evidence, 'failure.png'), fullPage: true });
  await writeFile(path.join(evidence, 'failure.txt'), String(error) + '\n' + await page.locator('body').innerText());
  throw error;
} finally {
  await context.close(); await browser.close();
}
