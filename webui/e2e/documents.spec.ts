import { test, expect } from '@playwright/test';
import { mockAPI } from './api';

for (const colorScheme of ['light', 'dark'] as const) for (const width of [390, 1280]) {
  test(`documents ${colorScheme} ${width}`, async ({ page }, testInfo) => {
    const api=await mockAPI(page);
    api.state.activeSettings.storageDir='<script>alert("path")</script>';
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
        await page.getByRole('button',{name:'存储、导入与搬迁说明',exact:true}).click();
        await expect(page.getByRole('link', { name: '容量收益图' })).toHaveAttribute('href', '/assets/storage-benefit.svg');
        await page.getByRole('button',{name:'备份与覆盖升级',exact:true}).click();
        await expect(page.getByText(/把 SWD.exe 和整个 licenses\/ 覆盖到原安装目录/)).toBeVisible();
        await expect(page.getByText(/build-info\.json|README\.txt|licenses\/TERMS\.txt/)).toHaveCount(0);
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
test('readonly terms has no service or hosts actions', async ({ page }) => {
 const api=await mockAPI(page);await page.goto('/terms');
 await expect(page.getByRole('article',{name:'完整使用条款'})).toBeVisible();
 await expect(page.getByRole('button',{name:/恢复.*直连|停止播放网关|同意/})).toHaveCount(0);
 expect(api.calls.some(c=>/^(hosts\/|start$|stop$)/.test(c.path))).toBe(false);
});
