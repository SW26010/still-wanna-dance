import { chromium, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const [baseURL, output, reason, fixture] = process.argv.slice(2);
await mkdir(output, {recursive:true});
const browser = await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page = await browser.newPage({viewport:{width:1280,height:900}}), errors = [], statusReads = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => {if(new URL(request.url()).pathname==='/api/status')statusReads.push(request.url());});
  await page.goto(baseURL);
  await expect(page.getByLabel('后台程序',{exact:true})).toContainText('启动失败');
  await expect(page.getByRole('group',{name:'SWD 启动失败',exact:true})).toContainText(reason);
  await expect(page.getByRole('link',{name:'修改设置',exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'退出程序',exact:true})).toBeEnabled();
  await page.screenshot({path:join(output,'recovery.png'),fullPage:true,animations:'disabled'});
  await page.setViewportSize({width:320,height:800});
  await expect(page.getByRole('group',{name:'SWD 启动失败',exact:true})).toContainText(reason);
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,'recovery-narrow.png'),fullPage:true,animations:'disabled'});
  await page.setViewportSize({width:1280,height:900});
  await page.goto(baseURL+'/#/about');
  await page.getByRole('tab',{name:'用户协议'}).click();
  await expect(page.getByText('本机已确认当前条款。')).toHaveCount(0);
  expect(errors).toEqual([]);
  expect(statusReads).toEqual([]);
  await page.goto(baseURL+'/#/settings');
  if(fixture==='contradictory'||fixture==='fields') {
    const budget=page.getByRole('spinbutton',{name:'视频存储预算（GiB）',exact:true});
    if(fixture==='contradictory') {
      await page.getByRole('tab',{name:'存储',exact:true}).click();
      await expect(budget).toHaveValue('0');
      await expect(page.getByRole('button',{name:'视频存储预算（GiB）：错误',exact:true})).toBeVisible();
      await budget.fill('1');
    } else {
      await expect(page.getByRole('combobox',{name:'程序日志级别',exact:true})).toHaveValue('LOUD');
      await page.getByRole('combobox',{name:'程序日志级别',exact:true}).selectOption('info');
      await page.getByRole('combobox',{name:'主题',exact:true}).selectOption('system');
      await page.getByRole('spinbutton',{name:'请求明细保留天数',exact:true}).fill('0');
      await page.getByRole('button',{name:'保存设置',exact:true}).click();
      await expect(page.getByRole('tab',{name:'存储',exact:true})).toHaveAttribute('aria-selected','true');
      await expect(budget).toBeFocused();
      await page.getByRole('tab',{name:'网络',exact:true}).click();
      await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('external-socks5');
      await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
      await expect(page.getByRole('spinbutton',{name:'同时下载数量',exact:true})).toHaveValue('65');
      await page.getByRole('spinbutton',{name:'同时下载数量',exact:true}).fill('8');
      await page.getByRole('tab',{name:'存储',exact:true}).click();
      await budget.fill('0');
    }
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
    await page.setViewportSize({width:320,height:800});
    await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:join(output,'repair-narrow.png'),fullPage:true,animations:'disabled'});
    await page.setViewportSize({width:1280,height:900});
    await page.screenshot({path:join(output,'repair-saved.png'),fullPage:true,animations:'disabled'});
    expect(statusReads).toEqual([]);
    const oldToken=await page.locator('meta[name="swd-token"]').getAttribute('content');
    await page.getByRole('button',{name:'重启并应用',exact:true}).click();
    await expect.poll(()=>page.locator('meta[name="swd-token"]').getAttribute('content'),{timeout:60000}).not.toBe(oldToken);
    const bootstrap=JSON.parse(await page.locator('#swd-bootstrap').textContent());
    expect(bootstrap.recovery).toBeUndefined();
    await page.screenshot({path:join(output,'restarted.png'),fullPage:true,animations:'disabled'});
    // Exit the unconsented replacement without enabling business services.
    const response=await page.request.post(baseURL+'/api/exit',{headers:{'X-SWD-Token':bootstrap.token},data:{}});
    expect(response.status()).toBe(200);
    await expect.poll(async()=>{try{return (await page.request.get(baseURL,{timeout:1000})).status();}catch{return 0;}},{timeout:15000}).toBe(0);
  } else {
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toHaveCount(0);
    await expect(page.getByText(/读取程序配置/)).toHaveCount(1);
    await page.screenshot({path:join(output,'settings-unavailable.png'),fullPage:true,animations:'disabled'});
    // A distinct editing failure must not be hidden with the startup reason.
    await page.route('**/api/recovery/settings',route=>route.fulfill({json:{unavailable:'配置文件被另一实例占用，不能修改设置。'}}));
    await page.reload();
    await expect(page.getByText(/读取程序配置/)).toHaveCount(1);
    await expect(page.getByText('配置文件被另一实例占用，不能修改设置。',{exact:true})).toBeVisible();
    await page.getByRole('button',{name:'退出程序',exact:true}).click();
    await expect(page.getByLabel('后台程序',{exact:true})).toContainText('未连接');
  }
  expect(errors).toEqual([]);
  await writeFile(join(output,'browser.json'),JSON.stringify({passed:true,errors},null,2));
  console.log('Recovery WebUI loaded and exited with invalid configuration.');
} finally { await browser.close(); }
