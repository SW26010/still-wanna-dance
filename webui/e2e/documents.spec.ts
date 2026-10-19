import { test, expect } from '@playwright/test';

for (const colorScheme of ['light', 'dark'] as const) for (const width of [390, 1280]) {
  test(`documents ${colorScheme} ${width}`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme }); await page.setViewportSize({ width, height: 900 });
    const errors: string[] = []; page.on('pageerror', e => errors.push(e.message));
    for (const route of ['/about', '/help', '/terms']) {
      await page.goto(route);
      await expect(page.locator('.fui-FluentProvider')).toBeVisible();
      await expect(page.locator('.brand-logo')).toBeVisible();
      expect(await page.locator('.brand-logo').evaluate((img: HTMLImageElement) => img.naturalWidth)).toBeGreaterThan(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.keyboard.press('Tab'); await expect(page.getByRole('link', { name: '跳到主要内容' })).toBeFocused();
      await page.keyboard.press('Enter'); await expect(page.locator('#main')).toBeFocused();
      if (route === '/help') {
        await expect(page.getByText('<script>alert("path")</script>', { exact: true })).toBeVisible();
        await expect(page.getByRole('link', { name: '容量收益图' })).toHaveAttribute('href', '/assets/storage-benefit.svg');
      }
      if (route === '/about') {
        await expect(page.getByText('dev-browser', {exact:true})).toBeVisible();
        await expect(page.getByText('a'.repeat(40), {exact:true})).toBeVisible();
      }
      await page.screenshot({ path: testInfo.outputPath(`${route.slice(1)}.png`), fullPage: true });
    }
    expect(errors).toEqual([]);
  });
}
test('readonly terms authenticates actions and displays server failures', async ({ page }) => {
  await page.goto('/terms');
  const req = page.waitForRequest('**/api/hosts/disable');
  await page.getByRole('button', { name: '恢复游戏直连' }).click();
  expect((await req).headers()['x-swd-token']).toBe('browser-fixture-token');
  await expect(page.getByText('hosts 恢复操作已完成。')).toBeVisible();
  await page.route('**/api/stop', route => route.fulfill({status: 500, body: '测试：无法停止'}));
  await page.getByRole('button', { name: '停止播放网关' }).click();
  await expect(page.getByText(/测试：无法停止/)).toBeVisible();
  await expect(page.getByRole('button', { name: '停止播放网关' })).toBeEnabled();
  await expect(page.getByRole('button', { name: /同意/ })).toHaveCount(0);
});
