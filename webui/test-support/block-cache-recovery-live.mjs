import {chromium,expect} from '@playwright/test';
import {mkdir} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output,cacheDir]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:900}}),errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(base+'/#storage-settings');
  await page.getByRole('button',{name:'后台状态'}).click();
  await expect(page.getByText('临时视频缓存目录不可用，视频服务和下载无法启动。',{exact:false})).toBeVisible();
  await expect(page.getByText(/修复后退出并重新打开应用/)).toBeVisible();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
  const field=page.getByRole('textbox',{name:'临时视频缓存目录',exact:true});
  await expect(field).toHaveValue(cacheDir);
  await page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true}).click();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  const response=await page.request.get(base+'/api/status',{headers:{'X-SWD-Token':token}});
  const status=await response.json();
  expect(status.settings.blockCacheDir).toBe('');
  expect(status.activeSettings.blockCacheDir).toBe(cacheDir);
  expect(status.workError).toContain('临时缓存盘不可用');
  expect(errors).toEqual([]);
  await page.screenshot({path:join(output,'cache-recovery.png'),fullPage:true});
} finally { await browser.close(); }
