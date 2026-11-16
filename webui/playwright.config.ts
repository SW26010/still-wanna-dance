import { defineConfig } from '@playwright/test';
import {browserChannel} from './test-support/browser.mjs';
export default defineConfig({
  testDir: './e2e', fullyParallel: true, reporter: 'list',
  workers: 2, retries: 0, forbidOnly: true,
  globalSetup: './test-support/browser.mjs',
  outputDir: '../artifacts/webui-tests',
  use: { baseURL: 'http://127.0.0.1:18473', channel: browserChannel, headless: true, screenshot: 'only-on-failure', trace: 'retain-on-failure' },
  webServer: { command: 'node test-support/fixture.mjs', url: 'http://127.0.0.1:18473/about', reuseExistingServer: false },
});
