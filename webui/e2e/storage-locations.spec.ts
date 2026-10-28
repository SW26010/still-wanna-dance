import {test, expect} from '@playwright/test';
import {mockAPI, choosePath} from './api';

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

test('canceling the database picker preserves drafts and permits another selection', async ({page}) => {
  const api=await mockAPI(page);
  let calls=0;
  await page.route('**/api/path/select', route => route.fulfill({json:++calls===1?{path:'',canceled:true}:{path:'D:\\Existing\\storage.sqlite',canceled:false}}));
  await page.goto('/#storage-settings');
  const open=page.getByRole('textbox',{name:'数据库文件',exact:true});
  await open.click(); await expect(open).toBeEnabled();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue(api.state.settings.databasePath);
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
  await expect(db).toHaveValue(api.state.defaultDatabasePath);
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
  await expect(dialog).toContainText('换数据库不会带入原数据库的收藏');
  await dialog.getByRole('button',{name:'取消',exact:true}).click();
  expect(api.state.settings.databasePath).toBe(original);
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await save.click();
  await dialog.getByRole('button',{name:'确认并保存',exact:true}).click();
  await expect(page.getByRole('button',{name:'重启并应用',exact:true})).toBeEnabled();
  expect(api.calls.find(c=>c.path==='settings')?.body).toMatchObject({confirmVideoPolicy:true,videoStorageMode:'none'});
  expect(api.state.settings).not.toHaveProperty('confirmVideoPolicy');
});

test('creation failure leaves the current database and video drafts intact',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/storage/prepare-database',r=>r.fulfill({status:400,body:'所选数据库无效，不会覆盖'}));
  await page.goto('/#storage-settings');
  await page.getByRole('textbox',{name:'数据库文件',exact:true}).click();
  await expect(page.getByText('所选数据库无效，不会覆盖',{exact:false})).toBeVisible();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue(api.state.settings.databasePath);
  await expect(page.getByRole('textbox',{name:'完整视频目录',exact:true})).toHaveValue(api.state.settings.videoDir);
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});

test('canceling a new database never creates it or changes the draft',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/path/select',r=>r.fulfill({json:{path:'',canceled:true}}));
  await page.goto('/#storage-settings');
  const create=page.getByRole('textbox',{name:'数据库文件',exact:true});
  await create.click(); await expect(create).toBeEnabled();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue(api.state.settings.databasePath);
  expect(api.calls.some(c=>c.path==='storage/prepare-database'||c.path==='settings')).toBe(false);
});

for(const target of [
  {anchor:'storage-settings',label:'完整视频目录',purpose:'video-directory'},
  {anchor:'download-settings',label:'VRChat 日志目录',purpose:'log-directory'},
  {anchor:'video-import',label:'外部目录',purpose:'import-directory'},
])test(`${target.label} uses a native directory picker and preserves the draft on cancel or failure`,async({page})=>{
  const api=await mockAPI(page);
  await page.setViewportSize({width:390,height:900});
  await page.goto('/#'+target.anchor);
  const field=page.getByRole('textbox',{name:target.label,exact:true});
  await expect(field).toHaveAttribute('readonly','');
  const original=await field.inputValue(), chosen='D:\\中文 目录\\选择结果';
  let calls=0;
  await page.route('**/api/path/select',r=>{
    expect(r.request().postDataJSON()).toEqual({purpose:target.purpose,path:calls<2?original:chosen});
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
  await expect(db).toHaveValue(api.state.defaultDatabasePath);
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
  await expect(db).toHaveValue(api.state.defaultDatabasePath);
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
  expect(api.state.settings).toMatchObject({manualLogDir:true,logDir:'D:\\Logs'});
  await reset.click();
  await expect(path).toHaveValue(api.state.defaultLogDir);
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(reset).toBeDisabled();
  await expect.poll(()=>api.state.settings.manualLogDir).toBe(false);
  expect(api.state.settings.logDir).toBe('');
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
