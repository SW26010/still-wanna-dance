// Production Console HTTP and a private hosts file; no mocked browser requests.
import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:850}});
  const errors=[];page.on('pageerror',error=>errors.push(error.message));
  await page.goto(baseURL+'/');
  const connection=page.getByRole('status',{name:'游戏连接',exact:true});
  const popup=page.getByRole('group',{name:'游戏连接说明',exact:true});
  await expect(connection).toHaveText('游戏连接：需检查');
  await connection.hover();
  await expect(popup).toContainText('尚未取得恢复权限，停止或退出时无法恢复直连。');
  await popup.getByRole('button',{name:'修复',exact:true}).click();
  await expect(popup.getByRole('button',{name:'修复',exact:true})).toBeEnabled();
  await expect(connection).toHaveText('游戏连接：需检查');
  await page.keyboard.press('Escape');
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'停止加速',exact:true})).toBeEnabled();
  await expect(connection).toHaveText('游戏连接：需检查');
  await page.getByRole('button',{name:'停止加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
  await expect(connection).toHaveText('游戏连接：需检查');
  await connection.hover();
  await expect(popup).toBeVisible();
  await expect(popup).toContainText('加速已停止，但未能恢复游戏直连');
  await page.screenshot({path:join(output,'repair-warning-live.png'),animations:'disabled'});
  await popup.getByRole('button',{name:'修复',exact:true}).click();
  await expect(connection).toHaveText('游戏连接：已接入');
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
  await page.screenshot({path:join(output,'repair-success-live.png'),animations:'disabled'});
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'停止加速',exact:true})).toBeEnabled();
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  await expect(page.getByRole('status',{name:'后台程序',exact:true})).toHaveText('后台程序：未连接');
  expect(errors).toEqual([]);
  await writeFile(join(output,'repair-browser.json'),JSON.stringify({passed:true,errors},null,2));
  console.log('Existing hosts: canceled repair, acceleration without reauthorization, stop failure, successful repair clearing the warning, resume and exit passed.');
} finally {
  await browser.close();
}
