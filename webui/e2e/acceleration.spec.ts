import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

test('acceleration status distinguishes connection from service without enabling the queue',async({page})=>{
  const api=await mockAPI(page);api.state.hosts.ready=true;api.state.queue={desired:false,running:false};
  await page.goto('/');
  await expect(page.getByText('加速未运行',{exact:true})).toBeVisible();
  await expect(page.getByText('游戏连接：已配置',{exact:true})).toBeVisible();
  await expect(page.getByText('本地服务：未启动',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'加速运行中',exact:true})).toBeDisabled();
  await expect(page.getByText('房间队列预加载：已关闭',{exact:true})).toBeVisible();
  expect(api.calls.filter(c=>/^(activation\/|hosts\/|queue\/|start$|stop$|exit$)/.test(c.path)).map(c=>c.path)).toEqual(['activation/enable']);
});

for(const restore of [true,false])test(`exit uses the active restore policy (${restore}) without a separate hosts request`,async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;api.state.queue={running:true,desired:true};api.state.activeSettings.restoreHostsOnExit=restore;api.state.settings.restoreHostsOnExit=!restore;
  await page.goto('/');
  await page.getByRole('button',{name:'退出整个应用',exact:true}).click();
  const dialog=page.getByRole('dialog');
  await expect(dialog).toContainText(restore?'退出时自动恢复本程序的游戏接入':'退出后保留游戏接入');
  await dialog.getByRole('button',{name:'确认',exact:true}).click();
  await expect(page.getByText('退出请求已接受，正在处理游戏连接并停止服务和后台任务。若恢复失败，程序会显示错误。',{exact:true})).toBeVisible();
  expect(api.calls.filter(c=>/^(activation\/|hosts\/|queue\/|start$|stop$|exit$)/.test(c.path)).map(c=>c.path)).toEqual(['exit']);
});

test('automatic gateway startup stays untested until request detection is started explicitly',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;
  await page.route('**/api/requests?*',route=>route.fulfill({json:{requests:[{at:new Date().toISOString(),status:200,outcome:'completed'}]}}));
  await page.goto('/');
  await expect(page.getByText('尚未检测游戏请求',{exact:true})).toBeVisible();
  await expect(page.getByText(/最近一次已结束请求：.*HTTP 200/)).toBeVisible();
  await expect(page.getByText(/尚未收到游戏请求/)).toHaveCount(0);
  expect(api.calls.some(c=>c.path==='activation/enable')).toBe(false);
  await page.getByRole('link',{name:'重新检测视频请求',exact:true}).click();
  await expect(page.getByRole('heading',{name:'网络与代理',level:1,exact:true})).toBeVisible();
  await page.getByRole('button',{name:'重新检测视频请求',exact:true}).click();
  await navigate(page,'游戏加速');
  await expect(page.getByText('检测已开始，尚未收到游戏请求；请在游戏中点歌。',{exact:true})).toBeVisible();
  expect(api.calls.filter(c=>/^(activation\/|hosts\/|queue\/|start$|stop$|exit$)/.test(c.path)).map(c=>c.path)).toEqual(['activation/enable']);
});

test('request observation reports a request received in the active detection window',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;
  api.state.activation={phase:'waiting',firstRequest:new Date().toISOString()};
  await page.goto('/');
  await expect(page.getByText('已收到游戏请求，请在游戏中确认播放',{exact:true})).toBeVisible();
  await expect(page.getByText(/尚未收到游戏请求/)).toHaveCount(0);
});

test('existing managed connection can prepare exit cleanup despite automatic gateway startup',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts={ready:true,managed:true,exitRestorePrepared:false,message:'已配置'};
  await page.goto('/');
  await expect(page.getByText(/已有游戏接入尚未准备自动恢复/).first()).toBeVisible();
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'加速运行中',exact:true})).toBeDisabled();
  expect(api.calls.filter(c=>c.path==='activation/enable')).toHaveLength(1);
});
