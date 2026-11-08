import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';

const [baseURL,fixture,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:1280,height:900}}),errors=[],statusReads=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('request',r=>{if(new URL(r.url()).pathname==='/api/status')statusReads.push(r.url());});
  await page.goto(baseURL+'/#/settings');
  for(const name of ['常规','存储','下载与缓存','网络'])await expect(page.getByRole('tab',{name,exact:true})).toBeEnabled();
  await expect(page.getByRole('checkbox',{name:'启动程序时自动打开 WebUI',exact:true})).toBeEnabled();
  await page.getByRole('combobox',{name:'主题',exact:true}).selectOption('dark');
  await page.getByRole('checkbox',{name:'停止加速或退出时恢复游戏直连',exact:true}).check();
  await page.getByRole('tab',{name:'存储',exact:true}).click();
  await page.getByRole('textbox',{name:'数据库文件',exact:true}).click();
  await expect(page.getByText('数据库已新建。保存设置并重启后使用。',{exact:true})).toBeVisible();
  await page.getByRole('textbox',{name:'本地视频目录',exact:true}).click();
  await page.getByRole('spinbutton',{name:'视频存储预算（GiB）',exact:true}).fill('1');
  await page.getByRole('tab',{name:'网络',exact:true}).click();
  await page.getByRole('combobox',{name:'上游连接',exact:true}).selectOption('proxy');
  await page.getByRole('combobox',{name:'代理后端',exact:true}).selectOption('mihomo');
  // Server-only validation must reveal another category, then wait for the
  // post-action snapshot before focusing its now-enabled input.
  await page.getByRole('tab',{name:'常规',exact:true}).click();
  let releaseRefresh,receivedRefresh;
  const heldRefresh=new Promise(resolve=>{releaseRefresh=resolve;});
  const refreshArrived=new Promise(resolve=>{receivedRefresh=resolve;});
  const holdRefresh=async route=>{
    const response=await route.fetch();receivedRefresh();
    await heldRefresh;await route.fulfill({response});
  };
  await page.route('**/api/recovery/settings',holdRefresh);
  try {
    const validation=page.waitForResponse(r=>r.url().endsWith('/api/settings')&&r.status()===400);
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    expect((await (await validation).json()).code).toBe('settings_validation');
    await refreshArrived;
    const invalid=page.getByRole('textbox',{name:'导入配置文件',exact:true});
    await expect(page.getByRole('tab',{name:'网络',exact:true})).toHaveAttribute('aria-selected','true');
    await expect(invalid).toBeDisabled();await expect(invalid).not.toBeFocused();
    releaseRefresh();
    await expect(invalid).toBeEnabled();await expect(invalid).toBeFocused();
  } finally {
    releaseRefresh();await page.unroute('**/api/recovery/settings',holdRefresh);
  }
  await page.locator('input[type=file][aria-label="选择配置文件"]').setInputFiles(fixture);
  // Save from a different category: all repair drafts are a single transaction.
  await page.getByRole('tab',{name:'常规',exact:true}).click();
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  const rejected=page.waitForResponse(r=>r.url().endsWith('/api/settings')&&r.status()===400);
  await save.click();
  expect((await (await rejected).json()).code).toBe('uac_canceled');
  await expect(save).toBeEnabled();
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  const snapshot=await (await page.request.get(baseURL+'/api/recovery/settings',{headers:{'X-SWD-Token':token}})).json();
  expect(snapshot.settings.videoBudgetBytes).toBe(0);
  expect(snapshot.settings.restoreHostsOnStopOrExit).toBe(false);
  await page.getByRole('tab',{name:'存储',exact:true}).click();
  await expect(page.getByRole('spinbutton',{name:'视频存储预算（GiB）',exact:true})).toHaveValue('1');
  await expect(page.getByRole('button',{name:'视频存储预算（GiB）：错误',exact:true})).toHaveCount(0);
  await save.click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  await expect(save).toBeDisabled();
  await page.getByRole('tab',{name:'常规',exact:true}).click();
  await expect(page.getByRole('combobox',{name:'主题',exact:true})).toHaveValue('dark');
  for(const name of ['常规','存储','下载与缓存','网络']) {
    await page.getByRole('tab',{name,exact:true}).click();
    await page.screenshot({path:join(output,name+'.png'),fullPage:true,animations:'disabled'});
  }
  await page.setViewportSize({width:320,height:800});
  await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,'narrow.png'),fullPage:true,animations:'disabled'});
  expect(errors).toEqual([]);expect(statusReads).toEqual([]);
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  await expect(page.getByLabel('后台程序',{exact:true})).toContainText('未连接');
  await writeFile(join(output,'browser.json'),JSON.stringify({passed:true,errors,statusReads},null,2));
  console.log('Shared settings created a database, imported Mihomo, retried canceled authorization and saved every category.');
}finally{await browser.close();}
