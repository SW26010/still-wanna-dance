import { test, expect } from '@playwright/test';
import { mockAPI } from './api';
import { navigate, navigateStep } from './navigation';

const description = '本地视频持久化存储预算，用于跨运行保留完整视频。选择「不保留」时预算为 0，不保留持久化视频文件，仍可使用视频块缓存。已有本地视频在保护结束后回收。实际磁盘占用可能超过预算。';

test('settings preserve all three storage choices and the entered budget across saves', async ({page}, info) => {
  const api = await mockAPI(page);
  await page.goto('/#/settings');
  const input = page.getByRole('spinbutton', {name:'视频存储预算（GiB）', exact:true});
  const none = page.getByRole('radio', {name:'不保留', exact:true});
  const limited = page.getByRole('radio', {name:'限制为', exact:true});
  const unlimited = page.getByRole('radio', {name:'不限制', exact:true});
  await expect(page.getByRole('radiogroup', {name:'视频存储预算',exact:true}).getByRole('radio')).toHaveCount(3);
  await expect(input).toHaveAccessibleDescription(description);
  await expect(unlimited).toBeChecked();
  await expect(input).toBeDisabled();
  await none.check();
  await expect(input).toBeDisabled();
  await expect(input).toHaveValue('0');
  await page.getByRole('button', {name:'保存设置', exact:true}).click();
  await expect(page.getByRole('button', {name:'立即重启并应用'})).toBeEnabled();
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
  await navigate(page,'本地视频');
  await navigate(page,'设置');
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

test('wizard defaults to no retention and applies that choice with the remembered budget', async ({page}, info) => {
  const api = await mockAPI(page);
  await page.setViewportSize({width:390,height:844});
  await page.goto('/#/setup');
  await navigateStep(page,'2. 存储');
  const input = page.getByRole('spinbutton', {name:'视频存储预算（GiB）', exact:true});
  const none = page.getByRole('radio', {name:'不保留', exact:true});
  await expect(input).toHaveAccessibleDescription(description);
  await expect(page.getByRole('radio', {name:'不限制', exact:true})).toBeChecked();
  await page.getByRole('button', {name:'恢复本页默认', exact:true}).click();
  await expect(none).toBeChecked();
  await expect(input).toBeDisabled();
  await expect(input).toHaveValue('0');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('wizard-none.png'),fullPage:true,animations:'disabled'});
  await page.getByRole('radio', {name:'限制为', exact:true}).check();
  await expect(input).toHaveValue('40');
  for (const invalid of ['0', '-1', '0.001', '8388608', '']) {
    await input.fill(invalid);
    await page.getByRole('button', {name:'下一步',exact:true}).click();
    await expect(input).toBeVisible();
    expect(api.calls.filter(c=>c.path==='setup/validate'&&c.body.page==='storage')).toHaveLength(0);
  }
  await input.fill('12.5');
  await none.check();
  await page.getByRole('button', {name:'下一步',exact:true}).click();
  await page.getByRole('button', {name:'上一步',exact:true}).click();
  await expect(none).toBeChecked();
  await page.getByRole('radio', {name:'限制为',exact:true}).check();
  await expect(input).toHaveValue('12.5');
  await none.check();
  await page.getByRole('button', {name:'下一步',exact:true}).click();
  await page.getByRole('button', {name:'下一步',exact:true}).click();
  await expect(page.getByText('不限制 → 不保留（视频存储预算为 0）',{exact:true})).toBeVisible();
  await page.getByRole('button', {name:'确认并应用',exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='setup/apply').length).toBe(1);
  expect(api.calls.find(c=>c.path==='setup/apply')?.body).toMatchObject({videoStorageMode:'none',maxCacheBytes:12.5*1073741824});
  expect(api.calls.find(c=>c.path==='setup/apply')?.body).not.toHaveProperty('cacheUnlimited');
});

test('no retention overrides the remembered budget and only blocks filling the library', async ({page}) => {
  const api = await mockAPI(page);
  api.state.activeSettings.videoStorageMode = 'none';
  await page.goto('/#/cache');
  await expect(page.getByText(/当前视频存储预算：0 GiB（不保留持久化视频文件）/)).toBeVisible();
  await navigate(page,'准备歌曲');
  await expect(page.getByRole('button',{name:'下载补齐',exact:true})).toBeDisabled();
  await expect(page.getByRole('button',{name:'检查缺失视频',exact:true})).toBeEnabled();
  await expect(page.getByRole('button',{name:'开启队列预加载',exact:true})).toBeEnabled();
});

test('settings reject invalid budgets and inactive choices recover from an invalid draft', async ({page}) => {
  const api = await mockAPI(page);
  await page.goto('/#/settings');
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
