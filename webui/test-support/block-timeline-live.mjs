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
  await page.getByRole('button', { name: '展开时间轴' }).click();
  const panel = page.locator('#block-demands'), canvas = panel.locator('.timeline-canvas');
  await expect(panel.getByText('事件已连接', { exact: true })).toBeVisible();
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
  await page.setViewportSize({ width: 1280, height: 600 });
  await page.locator('#activation').scrollIntoViewIfNeeded();
  await expect.poll(() => panel.evaluate(element => element.getBoundingClientRect().top >= innerHeight)).toBe(true);
  await expect.poll(async () => (await page.request.get(url + '/__timeline/streams')).json()).toBe(0);
  await expect(detail).toHaveCount(0);
  await canvas.scrollIntoViewIfNeeded();
  await page.setViewportSize({ width: 1280, height: 900 });
  await expect(detail).toContainText('播放请求 #');
  await page.request.post(url + '/__timeline/release');
  await expect(panel.locator('.timeline-legend')).toContainText('延续保护 4');
  await expect(panel).toContainText('需求占用 0 / 4096');
  await expect(panel.locator('.demand-row')).toHaveCount(0);
  await expect(detail).toContainText('已无使用需求，本地数据仍在延续保护期');
  await expect(detail).toContainText('沿用最后一次 need-by');
  await expect(detail).toContainText('块 1/4');
  await page.screenshot({ path: path.join(evidence, 'real-protection.png'), fullPage: true });
  await page.getByRole('button', { name: '关闭块详情' }).click();
  await page.getByRole('button', { name: '收起时间轴' }).click();
  await expect.poll(async () => (await page.request.get(url + '/__timeline/streams')).json()).toBe(0);
  expect(errors).toEqual([]);
  await writeFile(path.join(evidence, 'result.json'), JSON.stringify({ outcome: 'passed', browser: await browser.version(), flow: ['real HTTP Range download', 'SSE downloading and source details', 'zero SSE requests while offscreen', 'pinned details restored with four local protected blocks', 'zero SSE requests after collapse'], errors }, null, 2));
  console.log('Real console + scheduler + Range download + browser SSE lifecycle passed.');
} catch (error) {
  await page.screenshot({ path: path.join(evidence, 'failure.png'), fullPage: true });
  await writeFile(path.join(evidence, 'failure.txt'), String(error) + '\n' + await page.locator('body').innerText());
  throw error;
} finally {
  await context.close(); await browser.close();
}
