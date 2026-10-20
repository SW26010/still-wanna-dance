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
  await page.goto(baseURL+'/#service');
  await page.getByRole('button',{name:'修改 hosts',exact:true}).click();
  await expect(page.getByText('游戏连接配置已更新。',{exact:true})).toBeVisible();
  await page.goto(baseURL+'/#/settings');
  await page.getByRole('button',{name:'高级游戏接入设置',exact:true}).click();
  const restore=page.getByRole('checkbox',{name:'退出时恢复游戏直连',exact:true});
  if(phase==='default') {
    await expect(restore).toBeChecked();await restore.uncheck();
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect(page.getByRole('button',{name:'立即重启并应用',exact:true})).toBeEnabled();
    await page.reload();
    await page.getByRole('button',{name:'高级游戏接入设置',exact:true}).click();
    await expect(restore).not.toBeChecked();
  } else {
    await expect(restore).not.toBeChecked();
    await expect(page.getByRole('button',{name:'立即重启并应用',exact:true})).toHaveCount(0);
  }
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,phase+'-settings.png'),fullPage:true,animations:'disabled'});
  await page.getByRole('button',{name:'退出整个应用',exact:true}).click();
  await expect(page.getByRole('dialog')).toContainText(phase==='default'?'退出时自动恢复':'退出后保留游戏接入');
  await page.screenshot({path:join(output,phase+'-exit.png'),animations:'disabled'});
  await page.getByRole('dialog').getByRole('button',{name:'确认',exact:true}).click();
  await expect(page.getByText(/退出请求已接受，正在处理游戏连接/)).toBeVisible();
  expect(errors).toEqual([]);
  await writeFile(join(output,phase+'-browser.json'),JSON.stringify({phase,passed:true,errors},null,2));
  console.log('Real Console '+phase+' policy, persisted setting, reload and exit passed.');
} finally {
  await browser.close();
}
