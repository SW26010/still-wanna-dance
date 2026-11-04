import {test, expect} from '@playwright/test';
import {mockAPI, choosePath} from './api';
import {saveSettings} from './settings-actions';

test('database selection can wait beyond the ordinary request timeout', async ({page}) => {
  await mockAPI(page);
  await page.route('**/api/path/select', async route => {
    await new Promise(resolve => setTimeout(resolve, 16_000));
    await route.fulfill({json:{path:'D:\\Existing\\storage.sqlite',canceled:false}});
  });
  await page.goto('/#storage-settings');
  await page.getByRole('textbox',{name:'数据库文件',exact:true}).click();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue('D:\\Existing\\storage.sqlite',{timeout:20_000});
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toBeEnabled();
});

for (const target of [
  {endpoint:'storage/validate-location',label:'完整视频目录',path:'D:\\Slow Videos',message:'检查存储位置超时'},
  {endpoint:'storage/prepare-database',label:'数据库文件',path:'D:\\Slow Database\\storage.sqlite',message:'数据库准备超时，结果尚未确认'},
]) test(`${target.endpoint} timeout releases the picker and preserves the draft`,async({page})=>{
  test.setTimeout(25000);
  const api=await mockAPI(page);
  let release!:()=>void, entered!:()=>void, requests=0;
  const blocked=new Promise<void>(resolve=>{release=resolve;}), started=new Promise<void>(resolve=>{entered=resolve;});
  await page.route('**/api/'+target.endpoint,async route=>{
    requests++;entered();await blocked;
    await route.fulfill({json:target.endpoint==='storage/validate-location'?{valid:true}:{path:target.path,created:true}}).catch(()=>{});
  });
  try {
    await page.goto('/#storage-settings');
    await choosePath(page,target.label,target.path);
    await started;
    await expect(page.getByRole('button',{name:'放弃更改',exact:true})).toBeDisabled();
    if(target.endpoint==='storage/prepare-database') {
      api.state.settings.videoDir='D:\\Other Videos';
      await expect(page.getByRole('textbox',{name:'完整视频目录',exact:true})).toHaveValue('D:\\Other Videos',{timeout:10000});
    }
    await expect(page.getByText(target.message,{exact:false})).toBeVisible({timeout:19000});
    await expect(page.getByRole('textbox',{name:target.label,exact:true})).toHaveValue(target.path);
    await expect(page.getByRole('textbox',{name:target.label,exact:true})).toBeEnabled();
    await expect(page.getByRole('button',{name:'放弃更改',exact:true})).toBeEnabled();
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
    expect(requests).toBe(1);
    await page.getByRole('button',{name:'放弃更改',exact:true}).click();
    await expect(page.getByText(target.message,{exact:false})).toHaveCount(0);
  } finally { release(); }
});

test('pagehide cancels a pending location check without changing the draft',async({page})=>{
  await mockAPI(page);
  let release!:()=>void, entered!:()=>void;
  const blocked=new Promise<void>(resolve=>{release=resolve;}), started=new Promise<void>(resolve=>{entered=resolve;});
  await page.route('**/api/storage/validate-location',async route=>{
    entered();await blocked;await route.fulfill({json:{valid:true}}).catch(()=>{});
  });
  try {
    await page.goto('/#storage-settings');
    const field=page.getByRole('textbox',{name:'完整视频目录',exact:true}), original=await field.inputValue();
    await choosePath(page,'完整视频目录','D:\\Canceled Videos');
    await started;
    await page.evaluate(()=>dispatchEvent(new Event('pagehide')));
    await expect(field).toBeEnabled();
    await expect(field).toHaveValue(original);
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  } finally { release(); }
});

test('canceling the database picker preserves drafts and permits another selection', async ({page}) => {
  const api=await mockAPI(page);
  let calls=0;
  await page.route('**/api/path/select', route => route.fulfill({json:++calls===1?{path:'',canceled:true}:{path:'D:\\Existing\\storage.sqlite',canceled:false}}));
  await page.goto('/#storage-settings');
  const open=page.getByRole('textbox',{name:'数据库文件',exact:true});
  await open.click(); await expect(open).toBeEnabled();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue('.\\storage\\storage.sqlite');
  await open.click();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue('D:\\Existing\\storage.sqlite');
  expect(api.calls.some(c=>c.path==='settings'||c.path==='start')).toBe(false);
});

test('explicit database creation only updates the draft until save and restart', async ({page}, info) => {
  const api=await mockAPI(page);
  api.state.settings.databasePath=api.state.activeSettings.databasePath='';
  api.state.settings.videoDir=api.state.activeSettings.videoDir='';
  api.state.settings.videoStorageMode='none';
  await page.setViewportSize({width:390,height:900});
  await page.goto('/#storage-settings');
  const db=page.getByRole('textbox',{name:'数据库文件',exact:true});
  const videos=page.getByRole('textbox',{name:'完整视频目录',exact:true});
  await expect(db).toBeEnabled(); await expect(videos).toBeEnabled();
  await page.getByRole('textbox',{name:'数据库文件',exact:true}).click();
  await expect(db).toHaveValue('.\\storage\\storage.sqlite');
  await expect(videos).toHaveValue(api.state.defaultVideoDir);
  expect(api.calls.some(c=>c.path==='settings'||c.path==='start')).toBe(false);
  expect(api.state.activeSettings.databasePath).toBe('');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(api.state.settings.databasePath).toBe(api.state.defaultDatabasePath);
  expect(api.state.activeSettings.databasePath).toBe('');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('locations-mobile.png'),fullPage:true});
});

test('existing videos require a reviewable policy decision and cancellation preserves settings', async ({page}) => {
  const api=await mockAPI(page), original=api.state.settings.databasePath;
  await page.route('**/api/storage/check',r=>r.fulfill({json:{confirmVideoPolicy:true}}));
  await page.goto('/#storage-settings');
  await choosePath(page, '数据库文件', 'D:\\Library\\other.sqlite');
  await page.getByRole('radio',{name:'不保留',exact:true}).check();
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  await save.click();
  const dialog=page.getByRole('dialog');
  await expect(dialog).toContainText('不保留普通视频');
  await expect(dialog).toContainText('可能被删除');
  await expect(dialog).toContainText('优先保留收藏视频');
  await dialog.getByRole('button',{name:'取消',exact:true}).click();
  expect(api.state.settings.databasePath).toBe(original);
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await save.click();
  await dialog.getByRole('button',{name:'确认并保存',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(api.calls.find(c=>c.path==='settings')?.body).toMatchObject({confirmVideoPolicy:{databasePath:'D:\\Library\\other.sqlite',videoDir:api.state.settings.videoDir,videoStorageMode:'none',videoBudgetBytes:api.state.settings.videoBudgetBytes,retainFavorites:true},videoStorageMode:'none'});
  expect(api.state.settings).not.toHaveProperty('confirmVideoPolicy');
});

test('creation failure keeps the chosen database in the draft and blocks saving',async({page})=>{
  const api=await mockAPI(page);
  const invalid='D:\\Library\\invalid.sqlite';
  await page.route('**/api/storage/prepare-database',r=>r.fulfill({status:400,body:'所选数据库无效，不会覆盖'}));
  await page.goto('/#storage-settings');
  await choosePath(page,'数据库文件',invalid);
  await expect(page.getByText('所选数据库无效，不会覆盖',{exact:false})).toBeVisible();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue(invalid);
  await expect(page.getByRole('textbox',{name:'完整视频目录',exact:true})).toHaveValue(api.state.settings.videoDir);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});

test('canceling a new database never creates it or changes the draft',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/path/select',r=>r.fulfill({json:{path:'',canceled:true}}));
  await page.goto('/#storage-settings');
  const create=page.getByRole('textbox',{name:'数据库文件',exact:true});
  await create.click(); await expect(create).toBeEnabled();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue('.\\storage\\storage.sqlite');
  expect(api.calls.some(c=>c.path==='storage/prepare-database'||c.path==='settings')).toBe(false);
});

for(const target of [
  {anchor:'storage-settings',label:'完整视频目录',purpose:'video-directory'},
  {anchor:'download-settings',label:'临时视频缓存目录',purpose:'block-cache-directory'},
  {anchor:'download-settings',label:'VRChat 日志目录',purpose:'log-directory'},
  {anchor:'video-import',label:'外部目录',purpose:'import-directory'},
])test(`${target.label} uses a native directory picker and preserves the draft on cancel or failure`,async({page})=>{
  const api=await mockAPI(page);
  await page.setViewportSize({width:390,height:900});
  await page.goto('/#'+target.anchor);
  const field=page.getByRole('textbox',{name:target.label,exact:true});
  await expect(field).toHaveAttribute('readonly','');
  const original=await field.inputValue(), chosen='D:\\中文 目录\\选择结果';
  const initialPath=target.purpose==='block-cache-directory'?api.state.defaultBlockCacheDir:original;
  let calls=0;
  await page.route('**/api/path/select',r=>{
    expect(r.request().postDataJSON()).toEqual({purpose:target.purpose,path:calls<2?initialPath:chosen});
    calls++;
    return calls===1?r.fulfill({status:400,body:'选择器暂不可用'}):r.fulfill({json:calls===2?{path:chosen,canceled:false}:{path:'',canceled:true}});
  });
  const button=field;
  await button.focus(); await page.keyboard.press('Enter');
  await expect(page.getByText('选择器暂不可用',{exact:false})).toBeVisible();
  await expect(field).toHaveValue(original);
  await button.click(); await expect(field).toHaveValue(chosen);
  await button.click(); await expect(button).toBeEnabled(); await expect(field).toHaveValue(chosen);
  expect(api.calls.some(c=>c.path==='settings'||c.path==='import/start')).toBe(false);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('temporary cache location is staged separately and can return to default', async ({page}) => {
  const api=await mockAPI(page);
  await page.goto('/#download-settings');
  const field=page.getByRole('textbox',{name:'临时视频缓存目录',exact:true});
  const reset=page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true});
  await expect(field).toHaveValue('.\\work');
  await expect(reset).toBeDisabled();
  await choosePath(page,'临时视频缓存目录','E:\\SSD Cache');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(api.state.settings.blockCacheDir).toBe('E:\\SSD Cache');
  expect(api.state.activeSettings.blockCacheDir).toBe('');
  expect(api.state.settings.videoDir).toBe(api.state.activeSettings.videoDir);
  expect(api.calls.some(c=>c.path==='storage/check')).toBe(false);
  await reset.click();
  await expect(field).toHaveValue('.\\work');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.state.settings.blockCacheDir).toBe('');
  await expect(page.getByRole('region',{name:'设置操作',exact:true}).getByRole('status')).toHaveText('已生效');
});

test('selecting the default cache directory uses the default setting', async ({page}) => {
  const api=await mockAPI(page);
  api.state.settings.blockCacheDir=api.state.activeSettings.blockCacheDir='E:\\SSD Cache';
  await page.goto('/#download-settings');
  await choosePath(page,'临时视频缓存目录',api.state.defaultBlockCacheDir);
  await expect(page.getByRole('textbox',{name:'临时视频缓存目录',exact:true})).toHaveValue('.\\work');
  await expect(page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true})).toBeDisabled();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.state.settings.blockCacheDir).toBe('');
});

test('conflicting choices stay in the draft until the whole combination is valid', async ({page},info) => {
  const api=await mockAPI(page), cachePath='E:\\SWD\\cache', rejectedVideo=cachePath+'\\videos', database='D:\\Library\\deferred.sqlite';
  await page.setViewportSize({width:320,height:900});
  await page.route('**/api/storage/validate-location', async route => {
    const body=route.request().postDataJSON();
    expect(Object.keys(body).sort()).toEqual(['blockCacheDir','databasePath','selection','videoDir']);
    if(body.videoDir===rejectedVideo && body.blockCacheDir===cachePath)
      return route.fulfill({json:{valid:false,field:'video',message:'完整视频目录与临时视频缓存目录不能相同或互相包含'}});
    return route.fulfill({json:{valid:true}});
  });
  api.state.settings.blockCacheDir=cachePath;
  await page.goto('/#storage-settings');
  const video=page.getByRole('textbox',{name:'完整视频目录',exact:true});
  await choosePath(page,'完整视频目录',rejectedVideo);
  await expect(page.getByText('完整视频目录与临时视频缓存目录不能相同或互相包含')).toBeVisible();
  await expect(video).toHaveValue(rejectedVideo);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('invalid-storage-draft-320.png'),fullPage:true});
  await page.locator('#storage-settings form').evaluate(form=>form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await choosePath(page,'数据库文件',database);
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue(database);
  await expect(page.getByText('完整视频目录与临时视频缓存目录不能相同或互相包含')).toBeVisible();
  expect(api.calls.some(c=>c.path==='storage/prepare-database')).toBe(false);
  await choosePath(page,'完整视频目录','D:\\Other Videos');
  await expect(video).toHaveValue('D:\\Other Videos');
  await expect(page.getByText('完整视频目录与临时视频缓存目录不能相同或互相包含')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  expect(api.calls.some(c=>c.path==='storage/prepare-database')).toBe(false);
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.state.settings.databasePath).toBe(database);
  expect(api.calls.find(c=>c.path==='storage/prepare-database')?.body).toMatchObject({databasePath:database,videoDir:'D:\\Other Videos',blockCacheDir:cachePath});
});

test('restoring a conflicting default keeps it in the draft until corrected', async ({page}) => {
  const api=await mockAPI(page), chosen='E:\\SWD\\videos';
  await page.route('**/api/storage/validate-location', async route => {
    const body=route.request().postDataJSON();
    expect(Object.keys(body).sort()).toEqual(['blockCacheDir','databasePath','selection','videoDir']);
    if(body.selection==='video' && body.videoDir===api.state.defaultVideoDir)
      return route.fulfill({json:{valid:false,field:'video',message:'默认视频目录与临时缓存目录冲突'}});
    return route.fulfill({json:{valid:true}});
  });
  await page.goto('/#storage-settings');
  await choosePath(page,'完整视频目录',chosen);
  const video=page.getByRole('textbox',{name:'完整视频目录',exact:true});
  await expect(video).toHaveValue(chosen);
  await page.getByRole('button',{name:'完整视频目录：恢复默认',exact:true}).click();
  await expect(page.getByText('默认视频目录与临时缓存目录冲突')).toBeVisible();
  await expect(video).toHaveValue(api.state.defaultVideoDir);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await choosePath(page,'完整视频目录',chosen);
  await expect(video).toHaveValue(chosen);
  await expect(page.getByText('默认视频目录与临时缓存目录冲突')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});

test('an existing conflict stays on the field that first caused it', async ({page}) => {
  const api=await mockAPI(page);
  const video='E:\\SWD\\videos', cache='E:\\SWD';
  await page.route('**/api/storage/validate-location', async route => {
    const body=route.request().postDataJSON();
    if(body.videoDir===video && body.blockCacheDir===cache) {
      const field=body.selection==='cache'?'cache':'video';
      return route.fulfill({json:{valid:false,field,message:'完整视频目录与临时视频缓存目录不能相同或互相包含'}});
    }
    return route.fulfill({json:{valid:true}});
  });
  api.state.settings.blockCacheDir=cache;
  await page.goto('/#storage-settings');
  await choosePath(page,'完整视频目录',video);
  await expect(page.getByRole('textbox',{name:'完整视频目录',exact:true})).toHaveValue(video);
  const videoField=page.getByRole('textbox',{name:'完整视频目录',exact:true}).locator('xpath=ancestor::div[contains(@class,"fui-Field")][1]');
  await expect(videoField).toContainText('完整视频目录与临时视频缓存目录不能相同或互相包含');
  await choosePath(page,'数据库文件','D:\\Library\\deferred.sqlite');
  await expect(videoField).toContainText('完整视频目录与临时视频缓存目录不能相同或互相包含');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
});

test('database selection validates against the saved cache choice', async ({page}) => {
  const api=await mockAPI(page);
  const cachePath='E:\\SWD\\cache', database=cachePath+'\\library.sqlite';
  await page.route('**/api/storage/validate-location', async route => {
    const body=route.request().postDataJSON();
    if(body.databasePath===database) {
      expect(body.blockCacheDir).toBe(cachePath);
      return route.fulfill({json:{valid:false,field:'database',message:'数据库文件不能位于临时视频缓存目录内'}});
    }
    return route.fulfill({json:{valid:true}});
  });
  api.state.settings.blockCacheDir=cachePath;
  await page.goto('/#storage-settings');
  const db=page.getByRole('textbox',{name:'数据库文件',exact:true}), original=await db.inputValue();
  await choosePath(page,'数据库文件',database);
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内')).toBeVisible();
  await expect(db).toHaveValue(database);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(api.calls.some(c=>c.path==='storage/prepare-database')).toBe(false);
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();
  await expect(db).toHaveValue(original);
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内')).toHaveCount(0);
  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await choosePath(page,'临时视频缓存目录','E:\\Other Cache');
  await expect(page.getByRole('textbox',{name:'临时视频缓存目录',exact:true})).toHaveValue('E:\\Other Cache');
  expect(api.calls.some(c=>c.path==='storage/prepare-database')).toBe(false);
});

test('restoring a directory never creates a deferred database before save', async ({page}) => {
  const api=await mockAPI(page), cache='E:\\SWD\\cache', database=cache+'\\new.sqlite';
  await page.route('**/api/storage/validate-location', async route => {
    const body=route.request().postDataJSON();
    if(body.databasePath===database && body.blockCacheDir===cache)
      return route.fulfill({json:{valid:false,field:'database',message:'数据库文件不能位于临时视频缓存目录内'}});
    return route.fulfill({json:{valid:true}});
  });
  api.state.settings.blockCacheDir=cache;
  await page.goto('/#storage-settings');
  await choosePath(page,'数据库文件',database);
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内')).toBeVisible();
  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true}).click();
  await saveSettings(page,'已生效');
  await page.getByRole('tab',{name:'存储',exact:true}).click();
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  expect(api.calls.some(c=>c.path==='storage/prepare-database')).toBe(false);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.state.settings.databasePath).toBe(database);
  expect(api.calls.some(c=>c.path==='storage/prepare-database')).toBe(true);
});

test('a deferred database preparation failure keeps saving blocked until reselected', async ({page}) => {
  const api=await mockAPI(page);
  const cache='E:\\SWD\\cache', database=cache+'\\broken.sqlite';
  await page.route('**/api/storage/validate-location', async route => {
    const body=route.request().postDataJSON();
    if(body.databasePath===database && body.blockCacheDir===cache)
      return route.fulfill({json:{valid:false,field:'database',message:'数据库文件不能位于临时视频缓存目录内'}});
    return route.fulfill({json:{valid:true}});
  });
  await page.route('**/api/storage/prepare-database', async route => {
    const body=route.request().postDataJSON();
    return body.databasePath===database ? route.fulfill({status:400,body:'所选数据库无效，不会覆盖'})
      : route.fulfill({json:{path:body.databasePath,created:false}});
  });
  api.state.settings.blockCacheDir=cache;
  await page.goto('/#storage-settings');
  await choosePath(page,'数据库文件',database);
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内')).toBeVisible();
  await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
  await page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true}).click();
  await saveSettings(page,'已生效');
  await page.getByRole('tab',{name:'存储',exact:true}).click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByText('所选数据库无效，不会覆盖')).toBeVisible();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await choosePath(page,'完整视频目录','D:\\Another Video Directory');
  await expect(page.getByRole('textbox',{name:'完整视频目录',exact:true})).toHaveValue('D:\\Another Video Directory');
  await expect(page.getByText('所选数据库无效，不会覆盖')).toBeVisible();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await choosePath(page,'数据库文件','D:\\Library\\valid.sqlite');
  await expect(page.getByText('所选数据库无效，不会覆盖')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
});

test('a deferred database preparation timeout leaves the result unconfirmed', async ({page}) => {
  test.setTimeout(25000);
  const api=await mockAPI(page);
  const cache='E:\\SWD\\cache', database=cache+'\\slow.sqlite';
  let release!:()=>void, preparing!:()=>void, preparations=0;
  const waiting=new Promise<void>(resolve=>{release=resolve;}), started=new Promise<void>(resolve=>{preparing=resolve;});
  await page.route('**/api/storage/validate-location',route=>{
    const body=route.request().postDataJSON();
    return route.fulfill({json:body.databasePath===database && body.blockCacheDir===cache
      ? {valid:false,field:'database',message:'数据库文件不能位于临时视频缓存目录内'} : {valid:true}});
  });
  await page.route('**/api/storage/prepare-database',async route=>{
    preparations++;preparing();await waiting;
    await route.fulfill({json:{path:database,created:true}}).catch(()=>{});
  });
  try {
    api.state.settings.blockCacheDir=cache;
    await page.goto('/#storage-settings');
    await choosePath(page,'数据库文件',database);
    await expect(page.getByText('数据库文件不能位于临时视频缓存目录内')).toBeVisible();
    await page.getByRole('tab',{name:'下载与缓存',exact:true}).click();
    await page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true}).click();
    await saveSettings(page,'已生效');
    await page.getByRole('tab',{name:'存储',exact:true}).click();
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    await started;
    await expect(page.getByText('数据库准备超时，结果尚未确认',{exact:false})).toBeVisible({timeout:19000});
    await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue(database);
    await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
    await expect(page.getByRole('button',{name:'放弃更改',exact:true})).toBeEnabled();
    expect(preparations).toBe(1);
  } finally { release(); }
});

test('a picker validates against locations updated while it was open', async ({page}) => {
  const api=await mockAPI(page), cache='E:\\Shared', video=cache+'\\videos';
  let release!:()=>void, opened!:()=>void;
  const waiting=new Promise<void>(resolve=>{release=resolve;}), started=new Promise<void>(resolve=>{opened=resolve;});
  const checked:string[]=[];
  await page.route('**/api/path/select',async route=>{opened();await waiting;await route.fulfill({json:{path:cache,canceled:false}});});
  await page.route('**/api/storage/validate-location',route=>{
    const body=route.request().postDataJSON(); checked.push(body.videoDir);
    return route.fulfill({json:body.blockCacheDir===cache && body.videoDir===video
      ? {valid:false,field:'cache',message:'视频目录与缓存目录冲突'} : {valid:true}});
  });
  await page.goto('/#download-settings');
  await page.getByRole('textbox',{name:'临时视频缓存目录',exact:true}).click();
  await started;
  api.state.settings.videoDir=video;
  await expect(page.getByLabel('完整视频目录',{exact:true})).toHaveValue(video,{timeout:10_000});
  release();
  await expect(page.getByRole('textbox',{name:'临时视频缓存目录',exact:true})).toHaveValue(cache);
  await expect(page.getByText('视频目录与缓存目录冲突')).toBeVisible();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(checked.at(-1)).toBe(video);
});

test('a picker rechecks if locations change while validation is in flight', async ({page}) => {
  const api=await mockAPI(page), cache='E:\\Shared', video=cache+'\\videos';
  let release!:()=>void, checking!:()=>void;
  const waiting=new Promise<void>(resolve=>{release=resolve;}), started=new Promise<void>(resolve=>{checking=resolve;});
  const checked:string[]=[];
  await page.route('**/api/storage/validate-location',async route=>{
    const body=route.request().postDataJSON(); checked.push(body.videoDir);
    if(checked.length===1){checking();await waiting;}
    return route.fulfill({json:body.blockCacheDir===cache && body.videoDir===video
      ? {valid:false,field:'cache',message:'视频目录与缓存目录冲突'} : {valid:true}});
  });
  await page.goto('/#download-settings');
  await choosePath(page,'临时视频缓存目录',cache);
  await started;
  api.state.settings.videoDir=video;
  await expect(page.getByLabel('完整视频目录',{exact:true})).toHaveValue(video,{timeout:10_000});
  release();
  await expect(page.getByText('视频目录与缓存目录冲突')).toBeVisible();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(checked).toEqual([api.state.defaultVideoDir,video]);
});

test('saved location updates revalidate an existing draft and clear resolved warnings', async ({page}) => {
  const api=await mockAPI(page), cache='E:\\Shared', video=cache+'\\videos';
  await page.route('**/api/storage/validate-location',route=>{
    const body=route.request().postDataJSON();
    return route.fulfill({json:body.blockCacheDir===cache && body.videoDir===video
      ? {valid:false,field:'cache',message:'视频目录与缓存目录冲突'} : {valid:true}});
  });
  await page.goto('/#download-settings');
  await choosePath(page,'临时视频缓存目录',cache);
  await expect(page.getByRole('textbox',{name:'临时视频缓存目录',exact:true})).toHaveValue(cache);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  api.state.settings.videoDir=video;
  await expect(page.getByText('视频目录与缓存目录冲突')).toBeVisible({timeout:10_000});
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  api.state.settings.videoDir='D:\\Other Videos';
  await expect(page.getByLabel('完整视频目录',{exact:true})).toHaveValue('D:\\Other Videos',{timeout:10_000});
  await expect(page.getByText('视频目录与缓存目录冲突')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  await expect(page.getByRole('textbox',{name:'临时视频缓存目录',exact:true})).toHaveValue(cache);
});

test('a stale revalidation cannot replace the warning for newer saved locations', async ({page}) => {
  const api=await mockAPI(page), cache='E:\\Shared', conflict=cache+'\\videos', safe='D:\\Other Videos';
  let release!:()=>void, checking!:()=>void;
  const waiting=new Promise<void>(resolve=>{release=resolve;}), started=new Promise<void>(resolve=>{checking=resolve;});
  await page.route('**/api/storage/validate-location',async route=>{
    const body=route.request().postDataJSON();
    if(body.videoDir===conflict) {
      checking(); await waiting;
      await route.fulfill({json:{valid:false,field:'cache',message:'过期的位置冲突'}}).catch(()=>{});
      return;
    }
    await route.fulfill({json:{valid:true}});
  });
  await page.goto('/#download-settings');
  await choosePath(page,'临时视频缓存目录',cache);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  api.state.settings.videoDir=conflict;
  await started;
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  api.state.settings.videoDir=safe;
  await expect(page.getByLabel('完整视频目录',{exact:true})).toHaveValue(safe,{timeout:10_000});
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  release();
  await expect(page.getByText('过期的位置冲突')).toHaveCount(0);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
});

test('a newly conflicting saved cache prevents preparing a selected database', async ({page}) => {
  const api=await mockAPI(page), cache='E:\\Shared', database=cache+'\\library.sqlite';
  let release!:()=>void, checking!:()=>void, calls=0;
  const waiting=new Promise<void>(resolve=>{release=resolve;}), started=new Promise<void>(resolve=>{checking=resolve;});
  await page.route('**/api/storage/validate-location',async route=>{
    const body=route.request().postDataJSON();
    if(++calls===1){checking();await waiting;}
    await route.fulfill({json:body.blockCacheDir===cache && body.databasePath===database
      ? {valid:false,field:'database',message:'数据库文件不能位于临时视频缓存目录内'} : {valid:true}});
  });
  await page.goto('/#storage-settings');
  await choosePath(page,'数据库文件',database);
  await started;
  api.state.settings.blockCacheDir=cache;
  await expect(page.getByLabel('临时视频缓存目录',{exact:true})).toHaveValue(cache,{timeout:10_000});
  release();
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内')).toBeVisible();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue(database);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(api.calls.some(call=>call.path==='storage/prepare-database')).toBe(false);
});

test('a saved location change during database preparation is revalidated', async ({page}) => {
  const api=await mockAPI(page), cache='E:\\Shared', database=cache+'\\library.sqlite';
  let release!:()=>void, preparing!:()=>void, preparations=0;
  const waiting=new Promise<void>(resolve=>{release=resolve;}), started=new Promise<void>(resolve=>{preparing=resolve;});
  await page.route('**/api/storage/validate-location',route=>{
    const body=route.request().postDataJSON();
    return route.fulfill({json:body.blockCacheDir===cache && body.databasePath===database
      ? {valid:false,field:'database',message:'数据库文件不能位于临时视频缓存目录内'} : {valid:true}});
  });
  await page.route('**/api/storage/prepare-database',async route=>{
    preparations++;preparing();await waiting;
    await route.fulfill({json:{path:database,created:true}});
  });
  await page.goto('/#storage-settings');
  await choosePath(page,'数据库文件',database);
  await started;
  api.state.settings.blockCacheDir=cache;
  await expect(page.getByLabel('临时视频缓存目录',{exact:true})).toHaveValue(cache,{timeout:10_000});
  release();
  await expect(page.getByText('数据库文件不能位于临时视频缓存目录内')).toBeVisible();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(preparations).toBe(1);
});

test('reselecting an unchanged legacy location preserves unrelated changes', async ({page}) => {
  const api=await mockAPI(page), cache='D:\\Legacy', video=cache+'\\videos';
  api.state.settings.videoDir=api.state.activeSettings.videoDir=video;
  api.state.settings.blockCacheDir=api.state.activeSettings.blockCacheDir=cache;
  await page.route('**/api/storage/validate-location',route=>{
    const body=route.request().postDataJSON();
    const unchanged=body.videoDir===api.state.settings.videoDir && body.blockCacheDir===api.state.settings.blockCacheDir;
    return route.fulfill({json:unchanged?{valid:true}:{valid:false,field:'video',message:'位置冲突'}});
  });
  await page.goto('/#storage-settings');
  await page.getByRole('checkbox',{name:'优先保留收藏视频'}).uncheck();
  await choosePath(page,'完整视频目录',video);
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeEnabled();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect.poll(()=>api.state.settings.retainFavorites).toBe(false);
  const saved=api.calls.find(call=>call.path==='settings')?.body;
  expect(saved).not.toHaveProperty('videoDir');
  expect(saved).not.toHaveProperty('blockCacheDir');
});

test('unavailable temporary cache explains how to recover', async ({page}) => {
  const api=await mockAPI(page);
  api.state.settings.blockCacheDir=api.state.activeSettings.blockCacheDir='E:\\SSD Cache';
  api.state.workError='磁盘不可访问';
  await page.goto('/#download-settings');
  await page.getByRole('button',{name:'后台状态'}).click();
  await expect(page.getByText('临时视频缓存目录不可用，视频服务和下载无法启动。',{exact:false})).toBeVisible();
  await expect(page.getByText('原因：磁盘不可访问')).toBeVisible();
  await page.getByRole('button',{name:'临时视频缓存目录：恢复默认',exact:true}).click();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
});

test('cache failure explains how to restart after repairing the same directory', async ({page}) => {
  const api=await mockAPI(page);
  api.state.settings.blockCacheDir=api.state.activeSettings.blockCacheDir='E:\\SSD Cache';
  api.state.workError='磁盘不可访问';
  await page.goto('/#storage-settings');
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeDisabled();
  await page.getByRole('button',{name:'后台状态'}).click();
  await expect(page.getByText(/修复后退出并重新打开应用/)).toBeVisible();
});

for (const theme of ['light','dark'] as const) test(`path fields are read-only pickers with default colors and reset at 320px in ${theme}`,async({page},info)=>{
  const api=await mockAPI(page);
  api.state.settings.theme=api.state.activeSettings.theme=theme;
  await page.setViewportSize({width:320,height:900});
  await page.goto('/#storage-settings');
  const db=page.getByRole('textbox',{name:'数据库文件',exact:true});
  const reset=page.getByRole('button',{name:'数据库文件：恢复默认',exact:true});
  await expect(db).toHaveAttribute('readonly','');
  await expect(db).toHaveAttribute('aria-haspopup','dialog');
  await expect(reset).toBeDisabled();
  const defaultColor=await db.evaluate(e=>getComputedStyle(e).color);
  await db.focus(); await page.keyboard.type('cannotedit');
  await expect(db).toHaveValue('.\\storage\\storage.sqlite');
  expect(api.calls.some(c=>c.path==='path/select')).toBe(false);
  await page.route('**/api/path/select',r=>{
    expect(r.request().postDataJSON().purpose).toBe('database');
    return r.fulfill({json:{path:'D:\\中文目录\\manual.sqlite',canceled:false}});
  });
  await page.keyboard.press('Enter');
  await expect(db).toHaveValue('D:\\中文目录\\manual.sqlite');
  await expect(reset).toBeEnabled();
  expect(await db.evaluate(e=>getComputedStyle(e).color)).not.toBe(defaultColor);
  await page.screenshot({path:info.outputPath('path-fields-'+theme+'.png'),fullPage:true});
  await reset.click();
  await expect(db).toHaveValue('.\\storage\\storage.sqlite');
  await expect(reset).toBeDisabled();
  expect(await db.evaluate(e=>getComputedStyle(e).color)).toBe(defaultColor);
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await expect(page.getByRole('button',{name:'打开已有数据库…',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'新建数据库…',exact:true})).toHaveCount(0);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});

test('log selection enables manual mode and reset returns to automatic detection',async({page})=>{
  const api=await mockAPI(page);
  await page.goto('/#download-settings');
  const path=page.getByRole('textbox',{name:'VRChat 日志目录',exact:true});
  const reset=page.getByRole('button',{name:'VRChat 日志目录：恢复默认',exact:true});
  await expect(reset).toBeDisabled();
  await choosePath(page,'VRChat 日志目录','D:\\Logs');
  await expect(path).toHaveValue('D:\\Logs');
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(api.state.settings).toMatchObject({vrchatLogDir:'D:\\Logs'});
  await reset.click();
  await expect(path).toHaveValue(api.state.defaultLogDir);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(reset).toBeDisabled();
  await expect.poll(()=>api.state.settings.vrchatLogDir).toBe('');
  expect(api.calls.filter(call=>call.path==='settings').at(-1)?.body.vrchatLogDir).toBe('');
  await expect(page.getByRole('region',{name:'设置操作',exact:true}).getByRole('status')).toHaveText('已生效');
});

test('resetting the video import source only clears the draft',async({page})=>{
  const api=await mockAPI(page);
  await page.goto('/#video-import');
  const field=page.getByRole('textbox',{name:'外部目录',exact:true});
  await choosePath(page,'外部目录','D:\\Import');
  await expect(field).toHaveValue('D:\\Import');
  await page.getByRole('button',{name:'外部目录：恢复默认',exact:true}).click();
  await expect(field).toHaveValue('');
  await expect(page.getByRole('button',{name:'扫描并导入',exact:true})).toBeDisabled();
  expect(api.calls.some(c=>c.path==='import/start')).toBe(false);
});
