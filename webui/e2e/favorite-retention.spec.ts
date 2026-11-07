import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {saveSettings} from './settings-actions';

test('favorite retention is a saved storage preference and does not start downloads',async({page})=>{
  const api=await mockAPI(page);
  await page.goto('/#storage-settings');
  const option=page.getByRole('checkbox',{name:'优先保留收藏视频',exact:true});
  await expect(option).toBeVisible();
  await expect(option).toBeChecked();
  await option.uncheck();
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();
  await expect(option).toBeChecked();
  await option.uncheck();
  await saveSettings(page,'待重启');
  expect(api.calls.find(c=>c.path==='settings')?.body.retainFavorites).toBe(false);
  expect(api.state.activeSettings.retainFavorites).toBe(true);
  await page.reload();
  await expect(option).not.toBeChecked();
  expect(api.calls.some(c=>/^(batch\/|queue\/|songs$|activation\/)/.test(c.path)&&c.body)).toBe(false);
});

for(const retain of [true,false])test(`no retention disables favorites without losing the preference (${retain})`,async({page},info)=>{
  const api=await mockAPI(page);
  api.state.settings.videoStorageMode=api.state.activeSettings.videoStorageMode='none';
  api.state.settings.retainFavorites=api.state.activeSettings.retainFavorites=retain;
  await page.setViewportSize({width:retain?1280:390,height:900});
  await page.emulateMedia({colorScheme:retain?'light':'dark'});
  await page.goto('/#storage-settings');
  const option=page.getByRole('checkbox',{name:'优先保留收藏视频',exact:true});
  await expect(option).toBeDisabled();
  await expect(option).toBeChecked({checked:retain});
  for(const mode of ['限制为','不限制']){
    await page.getByRole('radio',{name:mode,exact:true}).check();
    await expect(option).toBeEnabled();
    await expect(option).toBeChecked({checked:retain});
  }
  await option.setChecked(!retain);
  await page.getByRole('radio',{name:'不保留',exact:true}).check();
  await expect(option).toBeDisabled();
  await expect(option).toBeChecked({checked:!retain});
  await saveSettings(page,'待重启');
  expect(api.state.settings).toMatchObject({videoStorageMode:'none',retainFavorites:!retain});
  expect(api.state.activeSettings.retainFavorites).toBe(retain);
  await page.reload();
  await expect(option).toBeDisabled();
  await expect(option).toBeChecked({checked:!retain});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath(`favorite-retention-${retain}.png`),fullPage:true,animations:'disabled'});
  await page.getByRole('radio',{name:'限制为',exact:true}).check();
  await expect(option).toBeEnabled();
  await expect(option).toBeChecked({checked:!retain});
  expect(api.calls.some(c=>/^(batch\/|queue\/|songs$|activation\/)/.test(c.path)&&Object.keys(c.body).length)).toBe(false);
});
