import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './e2e', fullyParallel: true, reporter: 'list',
  outputDir: '../artifacts/webui-tests',
  use: { baseURL: 'http://127.0.0.1:18473', channel: process.env.CI ? 'chromium' : 'msedge', headless: true, screenshot: 'only-on-failure' },
  webServer: { command: 'node fixture.mjs', url: 'http://127.0.0.1:18473/about', reuseExistingServer: false },
});
