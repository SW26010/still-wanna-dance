import {chromium,expect} from '@playwright/test';
import {mkdir,stat} from 'node:fs/promises';
import {dirname,join,sep} from 'node:path';

const [base,databasePath,videoDir,cacheDir,output]=process.argv.slice(2);
await mkdir(output,{recursive:true});
const browser=await chromium.launch({channel:process.env.CI?'chromium':'msedge'});
try {
  const page=await browser.newPage({viewport:{width:390,height:900}}), errors=[];
  page.on('pageerror',error=>errors.push(error.message));
  await page.goto(base+'/#download-settings');
  const database=page.getByLabel('数据库文件',{exact:true});
  const video=page.getByLabel('本地视频目录',{exact:true});
  const cache=page.getByRole('textbox',{name:'临时视频缓存目录',exact:true});
  await expect(database).toHaveValue('.'+sep+join('data','database','storage.sqlite'));
  await expect(video).toHaveValue('.'+sep+'videos');
  await expect(cache).toHaveValue('.'+sep+'work');
  const originalVideo=await video.inputValue(), defaultCache=await cache.inputValue();
  const status=await page.evaluate(()=>fetch('/api/status',{headers:{'X-SWD-Token':JSON.parse(document.getElementById('swd-bootstrap').textContent).token}}).then(r=>r.json()));
  const invalidCache=join(dirname(status.defaultBlockCacheDir),'custom'), nestedVideo=join(cacheDir,'nested-videos'), deferredDatabase=join(cacheDir,'new','library.sqlite');
  const exists=async path=>{try { await stat(path); return true; } catch(error) { if(error.code==='ENOENT') return false; throw error; }};
  const save=page.getByRole('button',{name:'保存设置',exact:true});

  // Two pages use the real Console; only the OS selection dialog is replaced.
  const other=await browser.newPage();
  other.on('pageerror',error=>errors.push(error.message));
  await other.goto(base+'/#storage-settings');
  let releasePicker, pickerOpened;
  const pickerWaiting=new Promise(resolve=>{releasePicker=resolve;}), pickerStarted=new Promise(resolve=>{pickerOpened=resolve;});
  await page.route('**/api/path/select',async route=>{
    pickerOpened();await pickerWaiting;
    await route.fulfill({json:{path:cacheDir,canceled:false}});
  },{times:1});
  await cache.click();
  await pickerStarted;
  await other.route('**/api/path/select',route=>route.fulfill({json:{path:nestedVideo,canceled:false}}),{times:1});
  await other.getByRole('textbox',{name:'本地视频目录',exact:true}).click();
  await expect(other.getByRole('textbox',{name:'本地视频目录',exact:true})).toHaveValue(nestedVideo);
  await other.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(video).toHaveValue(nestedVideo,{timeout:10000});
  releasePicker();
  await expect(cache).toHaveValue(cacheDir);
  await expect(page.getByText('临时视频缓存目录与完整视频目录不能相同或互相包含',{exact:false})).toBeVisible();
  await expect(save).toBeDisabled();
  await other.getByRole('button',{name:'本地视频目录：恢复默认',exact:true}).click();
  await other.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(video).toHaveValue(originalVideo,{timeout:10000});
  await expect(page.getByText('临时视频缓存目录与完整视频目录不能相同或互相包含',{exact:false})).toHaveCount(0);
  await expect(save).toBeEnabled();
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();
  await expect(cache).toHaveValue(defaultCache);
  await other.close();

  await cache.click();
  await expect(page.getByText('请选择程序文件夹之外的位置，或恢复默认位置',{exact:false})).toBeVisible();
  await expect(cache).toHaveValue(invalidCache);
  await expect(save).toBeDisabled();
  await cache.click();
  await expect(cache).toHaveValue(cacheDir);

  await save.click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  await page.getByRole('tab',{name:'存储',exact:true}).click();
  await video.click();
  await expect(page.getByText('完整视频目录与临时视频缓存目录不能相同或互相包含',{exact:false})).toBeVisible();
  await expect(video).toHaveValue(nestedVideo);
  await expect(page.getByLabel('临时视频缓存目录',{exact:true})).toHaveValue(cacheDir);
  await expect(save).toBeDisabled();
  await page.screenshot({path:join(output,'warning-video.png'),fullPage:true});

  await database.click();
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内',{exact:false})).toBeVisible();
  await expect(database).toHaveValue(deferredDatabase);
  await expect(save).toBeDisabled();
  expect(await exists(deferredDatabase)).toBe(false);

  await video.click();
  await expect(video).toHaveValue(videoDir);
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内',{exact:false})).toBeVisible();
  expect(await exists(deferredDatabase)).toBe(false);

  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true}).click();
  await expect(cache).toHaveValue(defaultCache);
  await save.click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await page.getByRole('tab',{name:'存储',exact:true}).click();
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内',{exact:false})).toHaveCount(0);
  expect(await exists(deferredDatabase)).toBe(false);
  await expect(save).toBeEnabled();
  await save.click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(await exists(deferredDatabase)).toBe(true);

  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await cache.click();
  await expect(page.getByText('临时视频缓存目录与完整视频目录不能相同或互相包含',{exact:false})).toBeVisible();
  await expect(cache).toHaveValue(dirname(cacheDir));
  await expect(save).toBeDisabled();

  await cache.click();
  await expect(cache).not.toHaveValue(dirname(cacheDir));
  await expect(save).toBeEnabled();
  await save.click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await page.getByRole('tab',{name:'存储',exact:true}).click();
  await database.click();
  await expect(database).toHaveValue(databasePath);
  await expect(page.getByText('数据库已新建。保存设置并重启后使用。',{exact:true})).toBeVisible();

  const containingCache=await page.getByLabel('临时视频缓存目录',{exact:true}).inputValue();
  await page.getByRole('button',{name:'本地视频目录：恢复默认',exact:true}).click();
  await expect(page.getByText('完整视频目录与临时视频缓存目录不能相同或互相包含',{exact:false})).toBeVisible();
  await expect(video).toHaveValue(originalVideo);
  await expect(page.getByLabel('临时视频缓存目录',{exact:true})).toHaveValue(containingCache);
  await expect(save).toBeDisabled();
  await video.click();
  await expect(video).toHaveValue(videoDir);
  await save.click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await cache.click();
  await expect(cache).toHaveValue(cacheDir);
  await page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true}).click();
  await expect(cache).toHaveValue(defaultCache);
  await cache.click();
  await expect(cache).toHaveValue(defaultCache);
  await expect(page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true})).toBeDisabled();
  await cache.click();
  await expect(cache).toHaveValue(cacheDir);
  await save.click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  expect(errors).toEqual([]);
  await page.screenshot({path:join(output,'storage-choices.png'),fullPage:true});
} finally {
  await browser.close();
}
