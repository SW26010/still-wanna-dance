import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const [baseURL, output] = process.argv.slice(2);
await mkdir(output, {recursive:true});
const browser = await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page = await browser.newPage({viewport:{width:1280,height:900}}), errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(baseURL);
  await expect(page.getByLabel('后台程序',{exact:true})).toContainText('读取失败');
  await expect(page.getByRole('button',{name:'退出程序',exact:true})).toBeEnabled();
  await page.screenshot({path:join(output,'recovery.png'),fullPage:true,animations:'disabled'});
  await page.goto(baseURL+'/#/about');
  await page.getByRole('tab',{name:'用户协议'}).click();
  await expect(page.getByText('本机已确认当前条款。')).toHaveCount(0);
  expect(errors).toEqual([]);
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  await writeFile(join(output,'browser.json'),JSON.stringify({passed:true,errors},null,2));
  console.log('Recovery WebUI loaded and exited with invalid configuration.');
} finally { await browser.close(); }
