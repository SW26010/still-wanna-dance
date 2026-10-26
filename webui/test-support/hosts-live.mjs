// Isolated production Console + private hosts file; no mocked HTTP responses.
import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const [baseURL, phase, output] = process.argv.slice(2);
await mkdir(output, {recursive:true});
const browser = await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:850}});
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  await page.goto(baseURL+'/');
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByText('视频加速已开启。',{exact:true})).toBeVisible();
  await page.goto(baseURL+'/#/settings');
  const restore=page.getByRole('checkbox',{name:'退出时恢复游戏直连',exact:true});
  if(phase==='default') {
    await expect(restore).toBeChecked();await restore.uncheck();
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
    await page.reload();
    await expect(restore).not.toBeChecked();
  } else {
    await expect(restore).not.toBeChecked();
    await restore.check();
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
  }
  const geometry=await page.evaluate(()=>({scroll:document.documentElement.scrollWidth,viewport:innerWidth,overflow:[...document.querySelectorAll('body *')].filter(e=>e.getBoundingClientRect().right>innerWidth+1).map(e=>({tag:e.tagName,classes:e.className,text:e.textContent?.slice(0,80),width:e.getBoundingClientRect().width}))}));
  expect(geometry.scroll,JSON.stringify(geometry)).toBeLessThanOrEqual(geometry.viewport);
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.screenshot({path:join(output,phase+'-settings.png'),fullPage:true,animations:'disabled'});
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  await expect(page.getByText('正在退出应用并处理游戏连接。',{exact:true})).toBeVisible();
  await page.screenshot({path:join(output,phase+'-exit.png'),animations:'disabled'});
  expect(errors).toEqual([]);
  await writeFile(join(output,phase+'-browser.json'),JSON.stringify({phase,passed:true,errors},null,2));
  console.log('Real Console '+phase+' policy, persisted setting, reload and exit passed.');
} finally {
  await browser.close();
}
