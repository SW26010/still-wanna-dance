import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({locale:'zh-CN',viewport:{width:390,height:844}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(base+'/#network-tests');
  await expect(page.getByText('检测上游：SOCKS5',{exact:true})).toBeVisible();
  await expect(page.getByRole('link',{name:'当前连接',exact:true})).toHaveCount(0);
  await expect(page.getByRole('combobox',{name:'检测上游',exact:true})).toHaveCount(0);
  await expect(page.getByText(/^最后测试：/)).toHaveCount(1);
  const response=page.waitForResponse(r=>r.url().endsWith('/api/network/connectivity/start')&&r.request().method()==='POST');
  await page.getByRole('button',{name:'开始连通性检测',exact:true}).click();
  const started=await response;
  expect(started.ok()).toBe(true);
  expect(started.request().postDataJSON()).toEqual({});
  await expect(page.getByRole('button',{name:'开始连通性检测',exact:true})).toBeEnabled();
  await expect(page.locator('.diagnostic-rows p')).toHaveCount(5);
  await expect(page.locator('.diagnostic-rows')).not.toContainText('连接失败');
  await expect(page.locator('.diagnostic-rows')).toContainText('响应时间');
  await expect(page.locator('.diagnostic-rows')).not.toContainText('估计');
  await expect(page.getByText(/^最后测试：/)).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.mouse.move(0,0);
  await page.getByRole('button',{name:'开始连通性检测',exact:true}).blur();
  await page.screenshot({path:join(output,'active-proxy.png'),fullPage:true});
  await page.goto(base+'/#/library');
  await expect(page.getByRole('table',{name:'曲库歌曲'})).toContainText('canonical Udon metadata');
  expect(errors).toEqual([]);
  await writeFile(join(output,'result.json'),JSON.stringify({passed:true,realConsole:true,upstream:'controlled HTTP fixture',screenshots:['active-proxy.png'],errors},null,2));
} finally {await browser.close();}
