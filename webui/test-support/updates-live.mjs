// Real Console and browser; the test Console supplies the Release response.
import {chromium,expect} from '@playwright/test';
import {mkdir} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(base);
  await expect(page.getByRole('listitem',{name:'发现新版本 v1.0.0',exact:true})).toBeVisible();
  await page.getByRole('link',{name:'查看更新',exact:true}).click();
  const version=page.getByRole('link',{name:'v1.0.0',exact:true});
  await expect(version).toHaveAttribute('href','https://github.com/SW26010/still-wanna-dance/releases/tag/v1.0.0');
  const second=await browser.newPage();
  await second.goto(base+'/#/about');
  await expect(second.getByRole('link',{name:'v1.0.0',exact:true})).toBeVisible();
  await expect(second.getByRole('listitem',{name:'发现新版本 v1.0.0',exact:true})).toHaveCount(0);
  await page.reload();
  await expect(version).toBeVisible();
  await expect(page.getByRole('listitem',{name:'发现新版本 v1.0.0',exact:true})).toHaveCount(0);
  const checked=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/update/check');
  await page.getByRole('button',{name:'检查更新',exact:true}).click();
  expect((await checked).status()).toBe(200);
  await expect(version).toBeVisible();
  await expect(page.getByRole('button',{name:'检查更新',exact:true})).toBeEnabled();
  await expect(page.getByRole('listitem',{name:'发现新版本 v1.0.0',exact:true})).toHaveCount(0);
  await page.screenshot({path:join(output,'about-update.png'),fullPage:true});
  expect(errors).toEqual([]);
  console.log('Real Console: startup notice, About link, two pages, reload and manual check passed.');
} finally {await browser.close();}
