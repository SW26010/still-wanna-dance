import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('favorite retention is a saved storage preference and does not start downloads',async({page})=>{
  const api=await mockAPI(page);
  await page.goto('/#storage-settings');
  const option=page.getByRole('checkbox',{name:'优先保留收藏视频',exact:true});
  await expect(option).toBeChecked();
  await option.uncheck();
  expect(api.calls.some(c=>c.path==='settings')).toBe(false);
  await page.getByRole('button',{name:'放弃更改',exact:true}).click();
  await expect(option).toBeChecked();
  await option.uncheck();
  await page.getByRole('button',{name:'保存设置',exact:true}).click();
  await expect(page.getByRole('button',{name:'保存设置',exact:true})).toBeDisabled();
  expect(api.calls.find(c=>c.path==='settings')?.body.retainFavorites).toBe(false);
  expect(api.state.activeSettings.retainFavorites).toBe(true);
  await page.reload();
  await expect(option).not.toBeChecked();
  expect(api.calls.some(c=>/^(batch\/|queue\/|songs$|activation\/)/.test(c.path)&&c.body)).toBe(false);
});
