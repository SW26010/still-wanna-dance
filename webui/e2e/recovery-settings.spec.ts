import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

for(const initialFailure of [false,true])test(`recovery retains a rejected draft through read failure (initial failure: ${initialFailure})`,async({page})=>{
  const api=await mockAPI(page);
  let failRead=initialFailure,failSave=true;
  const saves:unknown[]=[];
  await page.route('**/api/recovery/settings',route=>failRead
    ?route.fulfill({status:503,json:{error:'测试配置读取失败'}})
    :route.fulfill({json:api.state}));
  await page.route('**/api/settings',async route=>{
    const patch=route.request().postDataJSON();saves.push(patch);
    if(failSave){failRead=true;await route.fulfill({status:500,json:{error:'测试保存失败'}});}
    else{Object.assign(api.state.settings,patch);await route.fulfill({json:{ok:true}});}
  });
  await page.goto('/?recovery#/settings');
  const field=page.getByRole('combobox',{name:'程序日志级别',exact:true});
  const save=page.getByRole('button',{name:'保存设置',exact:true});
  const error=page.getByText('Error: 测试配置读取失败',{exact:true});
  if(initialFailure){
    await expect(error).toBeVisible();await expect(field).toHaveCount(0);
    failRead=false;
    await page.evaluate(()=>document.dispatchEvent(new Event('visibilitychange')));
  }
  await expect(field).toHaveValue('info');await field.selectOption('debug');
  await save.click();
  await expect(error).toBeVisible();
  await expect(field).toBeVisible();await expect(field).toHaveValue('debug');
  await expect(field).toBeDisabled();await expect(save).toBeDisabled();
  expect(saves).toEqual([{logLevel:'debug'}]);
  expect(api.state.settings.logLevel).toBe('info');
  failRead=false;
  await page.evaluate(()=>document.dispatchEvent(new Event('visibilitychange')));
  await expect(error).toBeHidden();await expect(field).toBeEnabled();
  await expect(field).toHaveValue('debug');await expect(save).toBeEnabled();
  failSave=false;await save.click();
  await expect(save).toBeDisabled();await expect(field).toBeEnabled();
  await expect(field).toHaveValue('debug');
  expect(saves).toEqual([{logLevel:'debug'},{logLevel:'debug'}]);
  expect(api.state.settings.logLevel).toBe('debug');
});
