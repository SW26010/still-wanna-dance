import {chromium,expect} from '@playwright/test';
import {mkdir,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const [base,databasePath,videoDir,output,phase,logDir]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:900}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(base+'/#storage-settings');
  const db=page.getByRole('textbox',{name:'数据库文件',exact:true}),videos=page.getByRole('textbox',{name:'完整视频目录',exact:true});
  if(phase==='configure') {
    await expect(db).not.toHaveValue('');
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();

    await page.getByRole('textbox',{name:'数据库文件',exact:true}).click();
    await expect(db).not.toHaveValue('');
    await expect(page.getByText('数据库已新建。保存设置并重启后使用。',{exact:true})).toBeVisible();
    await page.getByRole('textbox',{name:'完整视频目录',exact:true}).click();
    await expect(videos).toHaveValue(videoDir);
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  } else if(phase==='reopened') {
    await expect(videos).toHaveValue(videoDir);
    await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
    await expect(page.getByText('当前数据库：尚未指定',{exact:false})).toHaveCount(0);
    await page.getByRole('textbox',{name:'数据库文件',exact:true}).click();
    await expect(page.getByText('已选择数据库。保存设置并重启后使用。',{exact:true})).toBeVisible();
    await expect(db).toHaveValue(databasePath);
  }
  await expect(page.getByText('正在处理…',{exact:true})).toHaveCount(0);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:join(output,phase+'.png'),fullPage:true});

  await page.goto(base+'/#download-settings');
  const logs=page.getByRole('textbox',{name:'VRChat 日志目录',exact:true});
  const reset=page.getByRole('button',{name:'VRChat 日志目录：恢复默认',exact:true});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  const restart=page.getByRole('button',{name:'重启并应用',exact:true});
  const limit=page.getByRole('spinbutton',{name:'同时下载数量',exact:true});
  await expect(logs).toBeVisible();
  // First-run import suggestions and save receipts can cover the bottom field
  // on a narrow viewport. Dismiss them through their normal UI controls.
  const closeNotices=page.getByRole('button',{name:'关闭通知',exact:true});
  for(let remaining=await closeNotices.count();remaining>0;remaining=await closeNotices.count()) {
    await closeNotices.last().click();
    await expect(closeNotices).toHaveCount(remaining-1);
  }
  if(phase==='configure') {
    await expect(reset).toBeDisabled();
    await logs.click();
    await expect(logs).toHaveValue(logDir);
    await limit.fill('32');
    await save.click();
    await expect(save).toBeDisabled();
    await expect(restart).toBeEnabled();
    await page.reload();
    await expect(logs).toHaveValue(logDir);
  } else if(phase==='reopened') {
    await expect(logs).toHaveValue(logDir);
    await expect(restart).toBeDisabled();
    await reset.click();
    await save.click();
    await expect(save).toBeDisabled();
    await expect(restart).toBeEnabled();
  } else {
    await expect(reset).toBeDisabled();
    await expect(restart).toBeDisabled();
  }
  await expect(limit).toHaveValue('32');
  const token=await page.locator('meta[name="swd-token"]').getAttribute('content');
  const response=await page.request.get(base+'/api/status',{headers:{'X-SWD-Token':token}});
  expect(response.ok()).toBe(true);
  const state=await response.json();
  expect(state.settings.vrchatLogDir).toBe(phase==='configure'?logDir:'');
  expect(state.activeSettings.vrchatLogDir).toBe(phase==='reopened'?logDir:'');
  expect(state.activeSettings.downloadConcurrencySoftLimit).toBe(phase==='configure'?16:32);
  if(phase!=='configure') await expect(logs).toHaveValue(state.defaultLogDir);
  await page.screenshot({path:join(output,phase+'-logs.png'),fullPage:true});
  expect(errors).toEqual([]);
  await writeFile(join(output,phase+'.json'),JSON.stringify({phase,database:state.settings.databasePath,videos:state.settings.videoDir,logDirectory:state.settings.vrchatLogDir,activeLogDirectory:state.activeSettings.vrchatLogDir,downloadConcurrencySoftLimit:state.settings.downloadConcurrencySoftLimit,restartRequired:state.restartRequired,errors},null,2));
} finally { await browser.close(); }
