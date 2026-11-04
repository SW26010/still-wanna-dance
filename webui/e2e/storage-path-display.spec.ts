import { test, expect } from '@playwright/test';
import { mockAPI, choosePath } from './api';

for (const prefix of ['', 'night.json.d\\']) test(`default storage paths stay relative in settings and previews: ${prefix || 'canonical'}`, async ({page}) => {
  const api = await mockAPI(page);
  const database = 'C:\\UI-Test\\' + prefix + 'data\\database\\storage.sqlite';
  const videos = 'C:\\UI-Test\\' + prefix + 'videos';
  api.state.defaultDatabasePath = api.state.settings.databasePath = api.state.activeSettings.databasePath = database;
  api.state.defaultVideoDir = api.state.settings.videoDir = api.state.activeSettings.videoDir = videos;
  // The cache default belongs to the executable, even when it is elsewhere.
  api.state.portableDir = 'E:\\SWD'; api.state.defaultBlockCacheDir = 'E:\\SWD\\work';
  await page.goto('/#storage-settings');
  const db = page.getByRole('textbox', {name:'数据库文件', exact:true});
  const video = page.getByRole('textbox', {name:'完整视频目录', exact:true});
  await expect(db).toHaveValue('.\\' + prefix + 'data\\database\\storage.sqlite');
  await expect(video).toHaveValue('.\\' + prefix + 'videos');
  await expect(page.getByRole('button', {name:'数据库文件：恢复默认', exact:true})).toBeDisabled();
  await db.click();
  await expect(db).toBeEnabled();
  expect(api.calls.find(call => call.path === 'path/select')?.body.path).toBe(database);
  expect(api.calls.some(call => call.path === 'settings')).toBe(false);

  await choosePath(page, '数据库文件', 'D:\\Library\\custom.sqlite');
  await expect(db).toHaveValue('D:\\Library\\custom.sqlite');
  await page.getByRole('button', {name:'数据库文件：恢复默认', exact:true}).click();
  await expect(db).toHaveValue('.\\' + prefix + 'data\\database\\storage.sqlite');
  await expect(page.getByRole('button', {name:'保存设置', exact:true})).toBeDisabled();

  await page.getByRole('tab', {name:'下载与缓存', exact:true}).click();
  await expect(page.getByRole('textbox', {name:'临时视频缓存目录', exact:true})).toHaveValue('.\\work');
  // System-owned default log paths are absolute and retain their current display.
  await expect(page.getByRole('textbox', {name:'VRChat 日志目录', exact:true})).toHaveValue(api.state.defaultLogDir);
  await page.goto('/#video-import');
  await expect(page.getByText('本次导入到完整视频目录：.\\' + prefix + 'videos', {exact:true})).toBeVisible();
});

test('policy confirmation formats default paths while requests keep resolved paths', async ({page}) => {
  const api = await mockAPI(page);
  api.state.defaultVideoDir = 'C:\\UI-Test\\videos';
  await page.route('**/api/storage/check', route => route.fulfill({json:{confirmVideoPolicy:true}}));
  await page.goto('/#storage-settings');
  await page.getByRole('button', {name:'完整视频目录：恢复默认', exact:true}).click();
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('视频目录：.\\videos');
  await expect(dialog).toContainText('数据库：.\\storage\\storage.sqlite');
  await expect(dialog).not.toContainText('C:\\UI-Test');
  await dialog.getByRole('button', {name:'确认并保存', exact:true}).click();
  await expect(page.getByRole('button', {name:'重启并应用', exact:true})).toBeEnabled();
  expect(api.calls.find(call => call.path === 'settings')?.body).toMatchObject({
    videoDir:api.state.defaultVideoDir,
    confirmVideoPolicy:{databasePath:api.state.defaultDatabasePath, videoDir:api.state.defaultVideoDir},
  });
  await page.reload();
  await expect(page.getByRole('textbox', {name:'完整视频目录', exact:true})).toHaveValue('.\\videos');
  await choosePath(page, '完整视频目录', 'D:\\Custom Videos');
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  await expect(dialog).toContainText('视频目录：D:\\Custom Videos');
  await dialog.getByRole('button', {name:'取消', exact:true}).click();
  await page.getByRole('button', {name:'放弃更改', exact:true}).click();
  await expect(page.getByRole('textbox', {name:'完整视频目录', exact:true})).toHaveValue('.\\videos');
});
