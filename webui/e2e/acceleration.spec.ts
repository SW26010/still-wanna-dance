import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

test('failed acceleration reports rollback failure and the resulting real state',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/activation/enable',async route=>{
    const error='视频网关启动失败；未能撤销本次游戏接入：hosts 在接入后被修改';
    api.state.hosts.ready=true;api.state.running=false;api.state.activation={phase:'failed',error};
    await route.fulfill({status:400,json:{error}});
  });
  await page.goto('/');
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByText(/未能撤销本次游戏接入/).first()).toBeVisible();
  await expect(page.getByText('游戏连接：已接入',{exact:true})).toBeVisible();
  await expect(page.getByText('视频服务：未运行',{exact:true})).toBeVisible();
  await expect(page.getByRole('listitem',{name:'视频加速已开启。',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
});

test('acceleration status distinguishes connection from service without enabling the queue',async({page})=>{
  const api=await mockAPI(page);api.state.hosts.ready=true;api.state.queue={desired:false,running:false};
  await page.goto('/');
  await expect(page.getByText('加速未运行',{exact:true})).toBeVisible();
  await expect(page.getByText('游戏连接：已接入',{exact:true})).toBeVisible();
  await expect(page.getByText('视频服务：未运行',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'重新检测视频请求',exact:true})).toBeEnabled();
  await expect(page.locator('#activation').getByRole('button').filter({hasText:/开始加速|重新检测视频请求|正在处理/})).toHaveCount(1);
  await expect(page.getByRole('status',{name:'房间队列',exact:true})).toHaveText('房间队列：已关闭');
  expect(api.calls.filter(c=>/^(activation\/|hosts\/|queue\/|start$|stop$|exit$)/.test(c.path)).map(c=>c.path)).toEqual(['activation/enable']);
});

for(const restore of [true,false])test(`exit uses the active restore policy (${restore}) without a separate hosts request`,async({page},info)=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;api.state.queue={running:true,desired:true};api.state.activeSettings.restoreHostsOnExit=restore;api.state.settings.restoreHostsOnExit=!restore;
  await page.setViewportSize({width:restore?1280:320,height:900});await page.goto('/');
  const exit=page.getByRole('button',{name:'退出程序',exact:true}),tip=page.getByRole('tooltip');
  await exit.hover();await expect(tip).toContainText('所有服务和后台任务将停止。');
  await expect(tip).toContainText(restore?'退出时自动恢复本程序的游戏接入':'退出后保留游戏接入');
  await expect(exit).toHaveAccessibleDescription(/所有服务和后台任务将停止/);
  const box=await tip.boundingBox();expect(box!.x).toBeGreaterThanOrEqual(0);expect(box!.x+box!.width).toBeLessThanOrEqual(restore?1280:320);
  await page.screenshot({path:info.outputPath('exit-tooltip.png'),animations:'disabled'});
  expect(api.calls.some(c=>c.path==='exit')).toBe(false);
  await exit.click();await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.getByRole('listitem',{name:'正在退出应用并处理游戏连接。',exact:true})).toBeVisible();
  expect(api.calls.filter(c=>/^(activation\/|hosts\/|queue\/|start$|stop$|exit$)/.test(c.path)).map(c=>c.path)).toEqual(['exit']);
});

test('automatic gateway startup stays untested until request detection is started explicitly',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;
  await page.route('**/api/requests?*',route=>route.fulfill({json:{requests:[{at:new Date().toISOString(),status:200,outcome:'completed'}]}}));
  await page.goto('/');
  await expect(page.getByText('尚未检测游戏请求',{exact:true})).toBeVisible();
  await expect(page.getByText(/最近请求：.*HTTP 200/)).toBeVisible();
  await expect(page.getByText(/尚未收到游戏请求/)).toHaveCount(0);
  expect(api.calls.some(c=>c.path==='activation/enable')).toBe(false);
  await page.getByRole('button',{name:'重新检测视频请求',exact:true}).click();
  await expect(page.getByText('等待游戏请求，请在游戏中点歌',{exact:true})).toBeVisible();
  expect(api.calls.filter(c=>/^(activation\/|hosts\/|queue\/|start$|stop$|exit$)/.test(c.path)).map(c=>c.path)).toEqual(['activation/enable']);
});

test('request observation reports a request received in the active detection window',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;
  api.state.activation={phase:'waiting',firstRequest:new Date().toISOString()};
  await page.goto('/');
  await expect(page.getByText('已收到游戏请求',{exact:true})).toBeVisible();
  await expect(page.getByText(/尚未收到游戏请求/)).toHaveCount(0);
});

test('existing managed connection can prepare exit cleanup despite automatic gateway startup',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts={ready:true,managed:true,exitRestorePrepared:false,message:'已配置'};
  await page.goto('/');
  await expect(page.getByText(/游戏接入尚未准备退出恢复/).first()).toBeVisible();
  await page.getByRole('button',{name:'退出程序',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('直接退出会保留接入');
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'重新检测视频请求',exact:true})).toBeEnabled();
  await expect(page.getByText(/游戏接入尚未准备退出恢复/)).toHaveCount(0);
  expect(api.calls.filter(c=>c.path==='activation/enable')).toHaveLength(1);
});
