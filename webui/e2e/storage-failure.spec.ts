import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

test('storage fault stays visible across polling, pages, gateway actions and connection loss',async({page})=>{
  const api=await mockAPI(page);
  await page.clock.install();await page.goto('/#/home');
  const heading=page.getByText('本地存储故障，已停止新的下载。',{exact:true});
  await expect(heading).toHaveCount(0);
  const cause='local cache storage unavailable: open C:\\'+('long-storage-path-'.repeat(15))+'\\tmp\\download.part: Access is denied. <script>unsafe()</script>';
  api.state.storageError=cause;
  await page.clock.fastForward(5100);await expect(heading).toBeVisible();
  const reason=page.getByText('首个错误：'+cause,{exact:true});
  await expect(reason).toBeVisible();
  await expect(page.getByText(/修复后退出并重新打开整个应用/)).toBeVisible();
  await page.getByRole('button',{name:'启动播放网关',exact:true}).click();
  await page.getByRole('button',{name:'停止播放网关',exact:true}).click();
  await expect(reason).toBeVisible();
  await page.setViewportSize({width:320,height:800});
  for(const name of ['运行状态','本地缓存','准备歌曲','设置']){
    await navigate(page,name);await expect(reason).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  }
  await page.route('**/api/status',r=>r.fulfill({status:503,body:'offline'}));
  await page.clock.fastForward(5100);
  await expect(page.getByText('无法连接控制台，请检查程序是否仍在运行。',{exact:true})).toBeVisible();
  await expect(reason).toBeVisible();
  await page.unroute('**/api/status');
  delete api.state.storageError;
  await page.clock.fastForward(5100);await expect(heading).toHaveCount(0);
});
