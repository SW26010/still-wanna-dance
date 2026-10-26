import {test, expect} from '@playwright/test';
import {mockAPI} from './api';

test('database selection can wait beyond the ordinary request timeout', async ({page}) => {
  await mockAPI(page);
  await page.route('**/api/storage/select-database', async route => {
    await new Promise(resolve => setTimeout(resolve, 16_000));
    await route.fulfill({json:{path:'D:\\Existing\\storage.sqlite',canceled:false}});
  });
  await page.goto('/#storage-settings');
  await page.getByRole('button',{name:'打开已有数据库…',exact:true}).click();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue('D:\\Existing\\storage.sqlite',{timeout:20_000});
  await expect(page.getByRole('button',{name:'打开已有数据库…',exact:true})).toBeEnabled();
});

test('canceling the database picker preserves drafts and permits another selection', async ({page}) => {
  const api=await mockAPI(page);
  let calls=0;
  await page.route('**/api/storage/select-database', route => route.fulfill({json:++calls===1?{path:'',canceled:true}:{path:'D:\\Existing\\storage.sqlite',canceled:false}}));
  await page.goto('/#storage-settings');
  const open=page.getByRole('button',{name:'打开已有数据库…',exact:true});
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
  await page.getByRole('button',{name:'在默认位置新建数据库',exact:true}).click();
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
  await page.getByRole('textbox',{name:'数据库文件',exact:true}).fill('D:\\Library\\other.sqlite');
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
  await page.route('**/api/storage/create-database',r=>r.fulfill({status:400,body:'文件已存在，不会覆盖'}));
  await page.goto('/#storage-settings');
  await page.getByRole('button',{name:'在此新建数据库',exact:true}).click();
  await expect(page.getByText('文件已存在，不会覆盖',{exact:false})).toBeVisible();
  await expect(page.getByRole('textbox',{name:'数据库文件',exact:true})).toHaveValue(api.state.settings.databasePath);
  await expect(page.getByRole('textbox',{name:'完整视频目录',exact:true})).toHaveValue(api.state.settings.videoDir);
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
});
