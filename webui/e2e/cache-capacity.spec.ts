import { test, expect } from '@playwright/test';
import { mockAPI } from './api';
import { navigate, navigateStep } from './navigation';

test('settings save zero independently of unlimited and preserve active capacity until restart', async ({page}) => {
  const api = await mockAPI(page);
  await page.goto('/#/settings');
  const input = page.getByRole('spinbutton', {name:'缓存容量（GiB）', exact:true});
  await expect(page.getByRole('radio', {name:'不限制', exact:true})).toBeChecked();
  await expect(input).toBeDisabled();
  await page.getByRole('radio', {name:'限制为', exact:true}).check();
  await input.fill('0');
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  await expect(page.getByRole('button', {name:'立即重启并应用'})).toBeEnabled();
  expect(api.state.settings).toMatchObject({cacheUnlimited:false,maxCacheBytes:0});
  expect(api.state.activeSettings.cacheUnlimited).toBe(true);
  await page.reload();
  await expect(page.getByRole('radio', {name:'限制为', exact:true})).toBeChecked();
  await expect(input).toHaveValue('0');
  await page.getByRole('radio', {name:'不限制', exact:true}).check();
  await page.getByRole('radio', {name:'限制为', exact:true}).check();
  await expect(input).toHaveValue('0');
  await input.fill('12.5');
  await page.getByRole('button', {name:'放弃更改', exact:true}).click();
  await expect(input).toHaveValue('0');
  await page.getByRole('radio', {name:'不限制', exact:true}).check();
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  await expect(page.getByRole('button', {name:'保存设置', exact:true})).toBeDisabled();
  expect(api.state.settings).toMatchObject({cacheUnlimited:true,maxCacheBytes:0});
});

test('wizard restores zero, validates the same range, reviews and applies the explicit choice', async ({page}) => {
  const api = await mockAPI(page);
  Object.assign(api.state.settings, {cacheUnlimited:false,maxCacheBytes:0});
  await page.goto('/#/setup');
  await navigateStep(page,'2. 存储');
  const input = page.getByRole('spinbutton', {name:'缓存容量（GiB）', exact:true});
  await expect(page.getByRole('radio', {name:'限制为', exact:true})).toBeChecked();
  await expect(input).toHaveValue('0');
  for (const invalid of ['-1', '0.001', '8388608', '']) {
    await input.fill(invalid);
    await page.getByRole('button', {name:'下一步',exact:true}).click();
    await expect(input).toBeVisible();
    expect(api.calls.filter(c=>c.path==='setup/validate'&&c.body.page==='storage')).toHaveLength(0);
  }
  await input.fill('0');
  await page.getByRole('button', {name:'下一步',exact:true}).click();
  await page.getByRole('button', {name:'下一步',exact:true}).click();
  await expect(page.getByText('不限 → 0 GiB（不持久保留缓存）',{exact:true})).toBeVisible();
  await page.getByRole('button', {name:'确认并应用',exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='setup/apply').length).toBe(1);
  expect(api.calls.find(c=>c.path==='setup/apply')?.body).toMatchObject({cacheUnlimited:false,maxCacheBytes:0});
});

test('zero active budget is displayed correctly and only blocks filling the library', async ({page}) => {
  const api = await mockAPI(page);
  Object.assign(api.state.activeSettings, {cacheUnlimited:false,maxCacheBytes:0});
  await page.goto('/#/cache');
  await expect(page.getByText(/当前上限：0 GiB（不持久保留缓存）/)).toBeVisible();
  await navigate(page,'准备歌曲');
  await expect(page.getByRole('button',{name:'下载补齐',exact:true})).toBeDisabled();
  await expect(page.getByRole('button',{name:'检查缺失视频',exact:true})).toBeEnabled();
  await expect(page.getByRole('button',{name:'开启队列预缓存',exact:true})).toBeEnabled();
});

test('settings reject invalid budgets and choosing unlimited recovers from an invalid draft', async ({page}) => {
  const api = await mockAPI(page);
  await page.goto('/#/settings');
  await page.getByRole('radio',{name:'限制为',exact:true}).check();
  const input = page.getByRole('spinbutton',{name:'缓存容量（GiB）',exact:true});
  for (const invalid of ['-1', '0.001', '8388608', '']) {
    await input.fill(invalid);
    await page.getByRole('button',{name:'保存设置',exact:true}).click();
    expect(api.calls.filter(c=>c.path==='settings')).toHaveLength(0);
  }
  await page.getByRole('radio',{name:'不限制',exact:true}).check();
  await expect(input).toBeDisabled();
  await expect(input).toHaveValue('40');
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
});
