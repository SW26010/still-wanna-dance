import { test, expect } from '@playwright/test';

test('brand and favicon share the approved palettes across live theme changes', async ({ page }, testInfo) => {
  // API interception makes Playwright abort /favicon.ico. Use the public document,
  // which has the same theme provider and does not require mocked API responses.
  await page.goto('/help?gate');
  const logo = page.locator('.brand-logo');
  await expect(logo).toBeVisible();
  const favicon = page.locator('link[rel="icon"]');
  await expect(favicon).toHaveCount(1);
  await expect(logo).toHaveAttribute('src', '/assets/logo.svg');
  const palettes = {
    light: [[234, 255, 223, 255], [128, 222, 80, 255], [32, 85, 42, 255]],
    dark: [[0, 41, 9, 255], [172, 255, 137, 255], [234, 255, 223, 255]],
  };
  for (const colorScheme of ['light', 'dark', 'light'] as const) {
    await page.emulateMedia({ colorScheme });
    await expect(favicon).toHaveAttribute('href', colorScheme === 'dark' ? '/favicon.ico' : '/favicon-light.ico');
    const faviconColors = await favicon.evaluate(async (link: HTMLLinkElement) => {
      const img = new Image();
      img.src = link.href;
      await img.decode();
      const canvas = document.createElement('canvas');
      canvas.width = canvas.height = 1000;
      const ctx = canvas.getContext('2d')!;
      ctx.drawImage(img, 0, 0, 1000, 1000);
      return [[0, 0], [156, 500], [384, 240]].map(([x, y]) => [...ctx.getImageData(x, y, 1, 1).data]);
    });
    expect(faviconColors).toEqual(palettes[colorScheme]);
    await expect.poll(() => logo.evaluate(async (img: HTMLImageElement) => {
      await img.decode();
      const canvas = document.createElement('canvas');
      canvas.width = canvas.height = 1000;
      const ctx = canvas.getContext('2d')!;
      ctx.drawImage(img, 0, 0, 1000, 1000);
      return [[0, 0], [156, 500], [384, 240]].map(([x, y]) => [...ctx.getImageData(x, y, 1, 1).data]);
    })).toEqual(palettes[colorScheme]);
    await page.screenshot({ path: testInfo.outputPath(`branding-${colorScheme}.png`) });
  }
  await page.reload();
  await expect(favicon).toHaveAttribute('href', '/favicon-light.ico');
  await expect(logo).toBeVisible();
});
