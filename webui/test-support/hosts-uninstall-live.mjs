import {launchBrowser} from './browser.mjs';
import {expect} from '@playwright/test';
import {mkdir,writeFile,readFile} from 'node:fs/promises';
import {join} from 'node:path';

// Real Console, real permission-helper protocol and a private hosts file.
const [baseURL,output,hostsPath,failure]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await launchBrowser();
try {
  const page=await browser.newPage({viewport:{width:390,height:850}});
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  const original=await readFile(hostsPath,'utf8');
  await page.goto(baseURL+'/#/help');
  const restore=page.getByRole('button',{name:'恢复游戏直连',exact:true});
  await expect(restore).toBeEnabled();
  await restore.click();
  if(failure){
    const message=failure==='prepare'?'fixture helper preparation failed':'加速已停止，但未能恢复游戏直连：fixture restore failed';
    await expect(page.getByRole('listitem',{name:message,exact:true})).toBeVisible();
    await expect(restore).toBeEnabled();
    expect(await readFile(hostsPath,'utf8')).toBe(original);
    await expect(page.getByText('可在退出后保留游戏接入',{exact:true})).toHaveCount(0);
    await page.reload();await expect(restore).toBeEnabled();
    await expect(page.getByText('可在退出后保留游戏接入',{exact:true})).toHaveCount(0);
    expect(errors).toEqual([]);
    await writeFile(join(output,'uninstall-'+failure+'-browser.json'),JSON.stringify({passed:true,errors},null,2));
    console.log('Uninstall '+failure+' failure preserves hosts and does not offer the keep-connection suggestion, including after reload.');
  }else{
    await expect(page.getByRole('listitem',{name:'管理员授权已取消',exact:true})).toBeVisible();
    await expect(restore).toBeEnabled();
    expect(await readFile(hostsPath,'utf8')).toBe(original);
    await page.goto(baseURL+'/#/home');
    await expect(page.getByRole('button',{name:'停止加速',exact:true})).toBeEnabled();
    await page.goto(baseURL+'/#/help');
    await restore.focus();await page.keyboard.press('Enter');
    const success=page.getByRole('listitem',{name:'游戏直连已恢复，可以退出程序后卸载。',exact:true});
    await expect(success).toBeVisible();await expect(restore).toBeEnabled();
    const restored=await readFile(hostsPath,'utf8');
    expect(restored).toBe(original.replace('\r\n127.0.0.1 api.udon.dance # SWD managed\r\n',''));
    expect(restored).toContain('127.0.0.1 play.udon.dance # user');
    await expect(page.getByText('可在退出后保留游戏接入',{exact:true})).toBeHidden();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:join(output,'uninstall-restored-live.png'),animations:'disabled'});
    await restore.click();
    await expect(page.getByRole('listitem',{name:'未发现 SWD 接入，无需恢复；可以退出程序后卸载。',exact:true})).toBeVisible();
    await page.goto(baseURL+'/#/home');
    await expect(success).toHaveCount(0);
    await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
    await expect(page.getByText('视频服务：未运行',{exact:true})).toBeVisible();
    await expect(page.getByText('房间队列：已关闭',{exact:true})).toBeVisible();
    await page.getByRole('button',{name:'开始加速',exact:true}).click();
    await expect(page.getByRole('button',{name:'停止加速',exact:true})).toBeEnabled();
    await expect(page.getByText('可在退出后保留游戏接入',{exact:true})).toBeHidden();
    await page.goto(baseURL+'/#/help');await restore.click();
    await expect(success).toBeVisible();await expect(restore).toBeEnabled();
    expect(await readFile(hostsPath,'utf8')).toBe(restored);
    await page.goto(baseURL+'/#/settings');
    await expect(page.getByRole('checkbox',{name:'停止加速或退出时恢复游戏直连',exact:true})).not.toBeChecked();
    expect(errors).toEqual([]);
    await writeFile(join(output,'uninstall-browser.json'),JSON.stringify({passed:true,errors},null,2));
    console.log('Uninstall recovery: canceled authorization, recovery, no-op, helper reuse, untouched user hosts entries and unchanged exit policy passed.');
  }
} finally {
  await browser.close();
}
