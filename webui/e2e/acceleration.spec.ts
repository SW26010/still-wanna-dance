import {test,expect} from '@playwright/test';
import {mockAPI} from './api';

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

test('exit retains hosts and does not invoke connection or queue operations',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;api.state.queue={running:true,desired:true};
  await page.goto('/');
  await page.getByRole('button',{name:'退出整个应用',exact:true}).click();
  const dialog=page.getByRole('dialog');
  await expect(dialog).toContainText('hosts 映射会保留');
  await dialog.getByRole('button',{name:'确认',exact:true}).click();
  await expect(page.getByText('退出请求已接受。正在停止服务和后台任务，hosts 映射仍保留。',{exact:true})).toBeVisible();
  expect(api.calls.filter(c=>/^(activation\/|hosts\/|queue\/|start$|stop$|exit$)/.test(c.path)).map(c=>c.path)).toEqual(['exit']);
});
