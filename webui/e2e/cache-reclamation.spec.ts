import {test, expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate, navigateStep} from './navigation';

test('reclamation strategy follows storage only in auto and survives save, discard and navigation', async ({page}, info) => {
  const api = await mockAPI(page);
  await page.goto('/#storage-settings');await page.getByRole('button',{name:'高级存储设置',exact:true}).click();
  const group = page.getByRole('radiogroup', {name:'激进的缓存回收策略', exact:true});
  const auto = group.getByRole('radio', {name:'自动（默认）', exact:true});
  const on = group.getByRole('radio', {name:'开启', exact:true});
  const off = group.getByRole('radio', {name:'关闭', exact:true});
  const save = page.getByRole('button', {name:'保存设置', exact:true});
  await expect(group.getByRole('radio')).toHaveCount(3);
  await expect(group).toHaveAccessibleDescription('通常可以最小化硬盘和内存占用，但可能增加反复下载消耗的流量，若希望边下边存，请关闭此选项。');
  await expect(auto).toBeChecked();
  await expect(page.getByText(/^自动（当前关闭）：/)).toBeVisible();
  await page.getByRole('radio', {name:'不保留', exact:true}).check();
  await expect(page.getByText(/^自动（当前开启）：/)).toBeVisible();
  await off.check();
  await page.getByRole('radio', {name:'限制为', exact:true}).check();
  await expect(off).toBeChecked();
  await on.check();
  await page.getByRole('radio', {name:'不限制', exact:true}).check();
  await expect(on).toBeChecked();
  await navigate(page, '本地视频');
  await navigate(page, '本地视频');await page.getByRole('link',{name:'存储设置',exact:true}).click();
  await expect(on).toBeChecked();
  await save.click();
  await expect(save).toBeDisabled();
  expect(api.state.settings.aggressiveCacheReclamation).toBe('on');
  expect(api.state.activeSettings.aggressiveCacheReclamation).toBe('auto');
  await page.reload();await page.getByRole('button',{name:'高级存储设置',exact:true}).click();
  await expect(on).toBeChecked();
  await off.check();
  await page.getByRole('button', {name:'放弃更改', exact:true}).click();
  await expect(on).toBeChecked();
  await off.check();
  await save.click();
  await expect(save).toBeDisabled();
  await page.reload();await page.getByRole('button',{name:'高级存储设置',exact:true}).click();
  await expect(off).toBeChecked();
  await auto.check();
  await page.getByRole('radio', {name:'不保留', exact:true}).check();
  await save.click();
  await expect(save).toBeDisabled();
  expect(api.state.settings.aggressiveCacheReclamation).toBe('auto');
  await page.reload();await page.getByRole('button',{name:'高级存储设置',exact:true}).click();
  await expect(auto).toBeChecked();
  await expect(page.getByText(/^自动（当前开启）：/)).toBeVisible();
  await page.setViewportSize({width:390,height:844});
  await page.emulateMedia({colorScheme:'dark'});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.getByRole('group', {name:'本地存储', exact:true}).screenshot({path:info.outputPath('reclamation-settings.png'),animations:'disabled'});
});

for (const {saved, active} of [
  {saved:'auto', active:'auto'}, {saved:'on', active:'on'},
  {saved:'off', active:'off'}, {saved:'on', active:'auto'},
] as const) {
test(`wizard preserves hidden reclamation ${saved} (active ${active}) through reset and apply`, async ({page}, info) => {
  const api = await mockAPI(page);
  api.state.settings.aggressiveCacheReclamation = saved;
  api.state.activeSettings.aggressiveCacheReclamation = active;
  await page.goto('/#/setup');
  await navigateStep(page, '2. 存储');
  const wizard = page.locator('.wizard');
  await expect(wizard.getByRole('radiogroup', {name:'激进的缓存回收策略', exact:true})).toHaveCount(0);
  await expect(wizard.getByText(/^自动（当前/)).toHaveCount(0);
  await page.getByRole('button', {name:'恢复本页默认', exact:true}).click();
  await expect(wizard.getByRole('radio', {name:'不保留', exact:true})).toBeChecked();
  await page.getByRole('button', {name:'下一步', exact:true}).click();
  expect(api.calls.find(c=>c.path==='setup/validate'&&c.body.page==='storage')?.body).toMatchObject({aggressiveCacheReclamation:saved,videoStorageMode:'none'});
  await page.getByRole('button', {name:'上一步', exact:true}).click();
  await page.getByRole('radio', {name:'限制为', exact:true}).check();
  await page.getByRole('button', {name:'下一步', exact:true}).click();
  expect(api.calls.filter(c=>c.path==='setup/validate'&&c.body.page==='storage').at(-1)?.body).toMatchObject({aggressiveCacheReclamation:saved,videoStorageMode:'limited'});
  await page.getByRole('button', {name:'上一步', exact:true}).click();
  await expect(wizard.getByRole('radiogroup', {name:'激进的缓存回收策略', exact:true})).toHaveCount(0);
  await page.setViewportSize({width:390,height:844});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('reclamation-wizard.png'),animations:'disabled'});
  await page.getByRole('button', {name:'下一步', exact:true}).click();
  await page.getByRole('button', {name:'下一步', exact:true}).click();
  const review = wizard.locator('dl div').filter({has:page.locator('dt', {hasText:'激进的缓存回收策略'})});
  if (saved === active) await expect(review).toHaveCount(0);
  else await expect(review).toHaveText('激进的缓存回收策略自动（当前关闭） → 开启（此前已保存）');
  await page.getByRole('button', {name:'确认并应用', exact:true}).click();
  await expect.poll(()=>api.calls.filter(c=>c.path==='setup/apply').length).toBe(1);
  expect(api.calls.find(c=>c.path==='setup/apply')?.body).toMatchObject({aggressiveCacheReclamation:saved,videoStorageMode:'limited'});
});
}
