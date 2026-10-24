import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigateStep} from './navigation';

test('all WebUI surfaces use acceleration and exit instead of independent lifecycle controls',async({page})=>{
  const api=await mockAPI(page);
  const removed=/^(停止视频网关|启动视频网关|恢复游戏直连|恢复直连|配置游戏连接|启动队列预加载|停止队列预加载|启用房间队列|停止房间队列)$/;
  for(const path of ['/','/#network-access','/#/library','/terms','/help','/#/setup']){
    await page.goto(path);
    await expect(page.getByRole('heading',{level:1})).toBeVisible();
    await expect(page.getByRole('button',{name:removed})).toHaveCount(0);
  }
  await navigateStep(page,'4. 启用');
  await expect(page.getByRole('button',{name:removed})).toHaveCount(0);
  expect(api.calls.filter(call=>['start','stop','hosts/enable','hosts/disable','queue/start','queue/stop'].includes(call.path))).toHaveLength(0);
});
