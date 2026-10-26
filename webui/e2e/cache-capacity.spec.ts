import { test, expect } from '@playwright/test';
import { mockAPI } from './api';
import { navigate } from './navigation';

const description = '最多为下载完成的视频保留多少空间。1 GiB 约为 1.07 GB。';

test('settings keep the video directory editable without losing its saved or draft path', async ({page}, info) => {
  const api = await mockAPI(page);
  const savedPath = 'D:\\SWD\\storage';
  api.state.settings.videoDir = savedPath;
  api.state.settings.videoStorageMode = 'none';
  await page.emulateMedia({colorScheme:'dark'});
  await page.goto('/#storage-settings');
  const path = page.getByRole('textbox', {name:'完整视频目录', exact:true});
  await expect(path).toBeVisible();
  await expect(path).toBeEnabled();
  await expect(path).toHaveValue(savedPath);
  await page.locator('#storage-settings').screenshot({path:info.outputPath('settings-independent-directory.png'),animations:'disabled'});
  await page.getByRole('radio', {name:'限制为', exact:true}).check();
  await expect(path).toBeEnabled();
  const draftPath = 'D:\\SWD\\new-storage';
  await path.fill(draftPath);
  await page.getByRole('radio', {name:'不保留', exact:true}).check();
  await page.getByRole('radio', {name:'不限制', exact:true}).check();
  await expect(path).toBeEnabled();
  await expect(path).toHaveValue(draftPath);
  await page.getByRole('radio', {name:'不保留', exact:true}).check();
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  await expect(page.getByRole('button', {name:'保存设置', exact:true})).toBeDisabled();
  expect(api.calls.find(c=>c.path==='settings')?.body).toMatchObject({videoDir:draftPath,videoStorageMode:'none'});
  await page.reload();
  await expect(path).toBeEnabled();
  await expect(path).toHaveValue(draftPath);
});


test('settings preserve all three storage choices and the entered budget across saves', async ({page}, info) => {
  const api = await mockAPI(page);
  await page.goto('/#storage-settings');
  const input = page.getByRole('spinbutton', {name:'视频存储预算（GiB）', exact:true});
  const none = page.getByRole('radio', {name:'不保留', exact:true});
  const limited = page.getByRole('radio', {name:'限制为', exact:true});
  const unlimited = page.getByRole('radio', {name:'不限制', exact:true});
  await expect(page.getByRole('radiogroup', {name:'是否保留下载的视频',exact:true}).getByRole('radio')).toHaveCount(3);
  await page.getByRole('button',{name:'空间占用说明',exact:true}).focus(); await expect(page.getByRole('tooltip')).toContainText('1 GiB'); await input.focus();
  await expect(unlimited).toBeChecked();
  await expect(input).toBeDisabled();
  await none.check();
  await expect(input).toBeDisabled();
  await expect(input).toHaveValue('0');
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  await expect(page.getByRole('button', {name:'重启并应用'})).toBeEnabled();
  expect(api.state.settings).toMatchObject({videoStorageMode:'none',maxCacheBytes:40*1073741824});
  expect(api.state.activeSettings.videoStorageMode).toBe('unlimited');
  expect(api.calls.find(c=>c.path==='settings')?.body).not.toHaveProperty('cacheUnlimited');
  await page.reload();
  await expect(none).toBeChecked();
  await expect(input).toHaveValue('0');
  await page.locator('#maxCacheGiB').screenshot({path:info.outputPath('settings-none.png'),animations:'disabled'});
  await limited.check();
  await expect(input).toHaveValue('40');
  await input.fill('');
  await input.pressSequentially('0.25');
  await expect(limited).toBeChecked();
  await expect(input).toHaveValue('0.25');
  await input.fill('12.5');
  await none.check();
  await limited.check();
  await expect(input).toHaveValue('12.5');
  await unlimited.check();
  await limited.check();
  await expect(input).toHaveValue('12.5');
  await navigate(page,'曲库');
  await navigate(page,'曲库');await page.getByRole('link',{name:'存储设置',exact:true}).click();
  await expect(limited).toBeChecked();
  await expect(input).toHaveValue('12.5');
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  await expect(page.getByRole('button', {name:'保存设置', exact:true})).toBeDisabled();
  expect(api.state.settings).toMatchObject({videoStorageMode:'limited',maxCacheBytes:12.5*1073741824});
  await page.reload();
  await expect(limited).toBeChecked();
  await expect(input).toHaveValue('12.5');
  await none.check();
  await page.getByRole('button', {name:'放弃更改', exact:true}).click();
  await expect(limited).toBeChecked();
  await expect(input).toHaveValue('12.5');
  await unlimited.check();
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  await expect(page.getByRole('button', {name:'保存设置', exact:true})).toBeDisabled();
  expect(api.state.settings).toMatchObject({videoStorageMode:'unlimited',maxCacheBytes:12.5*1073741824});
  await page.reload();
  await expect(unlimited).toBeChecked();
});


test('no retention overrides the remembered budget while favorites can still be filled', async ({page}) => {
  const api = await mockAPI(page);
  api.state.activeSettings.videoStorageMode = 'none';
  await page.goto('/#/cache');
  await expect(page.getByRole('region',{name:'曲库概览'}).getByText('不保留',{exact:true})).toBeVisible();
  await navigate(page,'曲库');
  await expect(page.getByRole('button',{name:'下载缺失视频',exact:true})).toBeEnabled();
  await expect(page.getByRole('button',{name:'检查缺失视频',exact:true})).toBeEnabled();
  await navigate(page,'加速');await expect(page.getByRole('heading',{name:'房间队列',exact:true})).toBeVisible();
});

test('empty storage keeps actions and settings links available',async({page})=>{
  const api=await mockAPI(page);api.files.splice(0);api.state.activeSettings.videoStorageMode='none';api.state.settings.videoStorageMode='none';
  await page.goto('/#/library');await expect(page.getByRole('button',{name:'下载缺失视频',exact:true})).toBeEnabled();
  await page.getByRole('link',{name:'设置保留空间',exact:true}).click();await expect(page.locator('#maxCacheGiB')).toBeFocused();
  await expect(page.getByRole('radio',{name:'不保留',exact:true})).toBeChecked();
});

test('settings reject invalid budgets and inactive choices recover from an invalid draft', async ({page}) => {
  const api = await mockAPI(page);
  await page.goto('/#storage-settings');
  const limited = page.getByRole('radio',{name:'限制为',exact:true});
  await limited.check();
  const input = page.getByRole('spinbutton',{name:'视频存储预算（GiB）',exact:true});
  for (const invalid of ['0', '-1', '0.001', '8388608', '']) {
    await input.fill(invalid);
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    expect(api.calls.filter(c=>c.path==='settings')).toHaveLength(0);
  }
  await page.getByRole('radio',{name:'不限制',exact:true}).check();
  await expect(input).toBeDisabled();
  await expect(input).toHaveValue('40');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  await limited.check();
  await input.fill('-1');
  await page.getByRole('radio',{name:'不保留',exact:true}).check();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(api.state.settings).toMatchObject({videoStorageMode:'none',maxCacheBytes:0});
});
