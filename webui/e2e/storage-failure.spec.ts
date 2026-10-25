import {navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('persistent faults keep the layout stable and retain their cause across pages',async({page})=>{
  const api=await mockAPI(page);
  await page.clock.install();await page.goto('/#/home');
  const heading=page.getByText('后台程序：存储故障',{exact:true});
  await expect(heading).toHaveCount(0);
  const before=await page.locator('#activation').boundingBox();
  const cause='local storage unavailable: '+('long-storage-path-'.repeat(15))+' Access is denied. <script>unsafe()</script>';
  api.state.storageError=cause;
  await page.clock.fastForward(5100);await expect(heading).toBeVisible();
  expect((await page.locator('#activation').boundingBox())!.y).toBe(before!.y);
  const showCause=async()=>{
    await page.getByRole('button',{name:'后台状态',exact:true}).click();
    await expect(page.getByText('首个错误：'+cause,{exact:true})).toBeVisible();
    await expect(page.getByText(/修复后退出并重新打开整个应用/)).toBeVisible();
    await page.keyboard.press('Escape');
  };
  await showCause();
  await page.setViewportSize({width:320,height:800});
  for(const name of ['时间线','曲库','设置']){
    await navigate(page,name);await expect(heading).toBeVisible();await showCause();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  await page.route('**/api/status',r=>r.fulfill({status:503,body:'offline'}));
  await page.clock.fastForward(5100);
  await page.getByRole('button',{name:'后台状态',exact:true}).click();
  await expect(page.getByText('无法读取程序状态，请检查 SWD 是否仍在运行。',{exact:true})).toBeVisible();
  await expect(page.getByText('首个错误：'+cause,{exact:true})).toBeVisible();
  await page.keyboard.press('Escape');
  await page.unroute('**/api/status');delete api.state.storageError;
  await page.clock.fastForward(5100);await expect(heading).toHaveCount(0);
});
