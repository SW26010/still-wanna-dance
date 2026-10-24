import {navigateSettings,navigate} from './navigation';
import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

test('storage fault stays visible across polling, pages, gateway actions and connection loss',async({page})=>{
  const api=await mockAPI(page);
  await page.clock.install();await page.goto('/#/home');
  const heading=page.getByText('本地存储故障，已停止新的下载。',{exact:true});
  await expect(heading).toHaveCount(0);
  const cause='local storage unavailable: open C:\\'+('long-storage-path-'.repeat(15))+'\\tmp\\download.part: Access is denied. <script>unsafe()</script>';
  api.state.storageError=cause;
  await page.clock.fastForward(5100);await expect(heading).toBeVisible();
  const reason=page.getByText('首个错误：'+cause,{exact:true});
  await expect(reason).toBeVisible();
  await expect(page.getByText(/修复后退出并重新打开整个应用/)).toBeVisible();
  await navigateSettings(page,'网络');await page.getByRole('tab',{name:'高级接入',exact:true}).click();
  await page.getByRole('button',{name:'修复并开启加速',exact:true}).click();
  await page.getByRole('button',{name:'重新检测视频请求',exact:true}).click();
  await expect(reason).toBeVisible();
  await page.setViewportSize({width:320,height:800});
  for(const name of ['时间线','曲库','设置']){
    await navigate(page,name);await expect(reason).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  await page.route('**/api/status',r=>r.fulfill({status:503,body:'offline'}));
  await page.clock.fastForward(5100);
  await expect(page.getByText('无法读取程序状态，请检查 SWD 是否仍在运行。',{exact:true})).toBeVisible();
  await expect(reason).toBeVisible();
  await page.unroute('**/api/status');
  delete api.state.storageError;
  await page.clock.fastForward(5100);await expect(heading).toHaveCount(0);
});
