import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './e2e', fullyParallel: true, reporter: 'list',
  outputDir: '../artifacts/webui-tests',
  use: { baseURL: 'http://127.0.0.1:18473', channel: process.env.CI ? 'chromium' : 'msedge', headless: true, screenshot: 'only-on-failure', trace: process.env.CI ? 'retain-on-failure' : 'off' },
  webServer: { command: 'node test-support/fixture.mjs', url: 'http://127.0.0.1:18473/about', reuseExistingServer: false },
});
