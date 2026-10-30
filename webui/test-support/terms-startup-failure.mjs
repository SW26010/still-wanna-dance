import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [base,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({locale:'zh-CN'});
  await page.goto(base+'/');
  const dialog=page.getByRole('alertdialog');
  await expect(dialog).toBeVisible();
  const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/terms/accept');
  await dialog.getByRole('button',{name:'同意完整条款'}).click();
  const result=await (await response).json();
  expect(result.ok).toBe(true);expect(result.warning).toContain('条款已保存');expect(result.warning).toContain('重启');
  await expect(dialog).toHaveCount(0);
  await expect(page.locator('.fui-ToastTitle').filter({hasText:result.warning})).toBeVisible();
  await page.getByRole('link',{name:'设置',exact:true}).click();
  await page.getByRole('tab',{name:'网络',exact:true}).click();
  await expect(page.getByText('代理启动失败，请检查节点和日志，修复后重启程序')).toBeVisible();
  await page.screenshot({path:join(output,'startup-failed-settings.png'),fullPage:true,animations:'disabled'});
  const current=await (await page.request.get(base+'/api/terms/current')).json();
  expect(current.accepted).toBe(true);
  await writeFile(join(output,'result.json'),JSON.stringify({ok:true,consentSaved:current.accepted,warningShown:true},null,2));
  console.log('Real Console consent + startup failure browser check passed.');
} finally {await browser.close();}
