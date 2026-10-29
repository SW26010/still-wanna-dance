import {test,expect} from '@playwright/test';
import {mockAPI} from './api';
import {navigate} from './navigation';

test('failed acceleration reports rollback failure and the resulting real state',async({page})=>{
  const api=await mockAPI(page);
  await page.route('**/api/activation/enable',async route=>{
    const error='视频网关启动失败；未能撤销本次游戏接入：hosts 在接入后被修改';
    api.state.hosts.ready=true;api.state.running=false;api.state.cdnError='视频网关启动失败';api.state.activation={phase:'failed',error,connectionError:'未能撤销本次游戏接入：hosts 在接入后被修改'};
    await route.fulfill({status:400,json:{error}});
  });
  await page.goto('/');
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('listitem',{name:/未能撤销本次游戏接入/})).toBeVisible();
  await expect(page.getByText('游戏连接：需检查',{exact:true})).toBeVisible();
  await expect(page.getByText('视频服务：异常',{exact:true})).toBeVisible();
  await page.getByRole('status',{name:'游戏连接',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('未能撤销本次游戏接入');
  await page.getByRole('status',{name:'视频服务',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toHaveText('视频网关启动失败');
  await expect(page.getByRole('listitem',{name:'视频加速已开启。',exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
});

test('acceleration status distinguishes connection from service without enabling the queue',async({page})=>{
  const api=await mockAPI(page);api.state.hosts.ready=true;api.state.activeSettings.queueWithGateway=false;api.state.queue={desired:false,running:false};
  await page.goto('/');
  await expect(page.getByText('加速未运行',{exact:true})).toBeVisible();
  await expect(page.getByText('游戏连接：已接入',{exact:true})).toBeVisible();
  await expect(page.getByText('视频服务：未运行',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'停止加速',exact:true})).toBeEnabled();
  await expect(page.locator('#activation').getByRole('button').filter({hasText:/开始加速|停止加速/})).toHaveCount(1);
  expect(api.state.queue.desired).toBe(false);
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
  await expect(page.getByRole('heading',{name:'加速运行中',exact:true})).toBeVisible();
  await expect(page.getByText('尚未检测游戏请求',{exact:true})).toHaveCount(0);
  await expect(page.getByText(/最近请求：.*HTTP 200/)).toBeVisible();
  await expect(page.getByText(/尚未收到游戏请求/)).toHaveCount(0);
  expect(api.calls.some(c=>c.path==='activation/enable')).toBe(false);
  await navigate(page,'网络');
  await page.getByRole('button',{name:'重新检测视频请求',exact:true}).click();
  await expect(page.getByRole('button',{name:'重新检测视频请求',exact:true})).toBeEnabled();
  await expect(page.getByText('等待游戏请求，请在游戏中点歌',{exact:true})).toHaveCount(0);
  expect(api.calls.filter(c=>/^(activation\/|hosts\/|queue\/|start$|stop$|exit$)/.test(c.path)).map(c=>c.path)).toEqual(['activation/enable']);
});

test('request observation details remain in connection diagnostics',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;
  api.state.activation={phase:'waiting',started:new Date().toISOString(),firstRequest:new Date().toISOString()};
  await page.goto('/');
  await expect(page.getByText('可关闭浏览器，减少资源占用',{exact:true})).toBeVisible();
  await expect(page.getByText('已收到游戏请求',{exact:true})).toHaveCount(0);
  await expect(page.getByText(/尚未收到游戏请求/)).toHaveCount(0);
  await navigate(page,'网络');
  await expect(page.locator('#service').getByText(/检测开始：.*首个请求：/)).toBeVisible();
});

for(const width of [320,1280])test(`existing connection accelerates without permission and repairs from its status (${width})`,async({page},info)=>{
  const api=await mockAPI(page);api.state.hosts={ready:true,managed:true,exitRestorePrepared:false,message:'已配置'};
  await page.setViewportSize({width,height:850});
  await page.emulateMedia({colorScheme:width===320?'dark':'light'});
  await page.goto('/');
  const connection=page.getByRole('status',{name:'游戏连接',exact:true});
  await expect(connection).toHaveText('游戏连接：需检查');await connection.hover();
  const popup=page.getByRole('group',{name:'游戏连接说明',exact:true});
  await expect(popup).toContainText('尚未取得恢复权限，停止或退出时无法恢复直连。');
  await expect(popup.getByRole('button',{name:'修复',exact:true})).toBeVisible();
  await page.screenshot({path:info.outputPath('restore-permission.png'),animations:'disabled'});
  expect(api.calls.some(c=>c.path==='hosts/prepare')).toBe(false);
  await page.keyboard.press('Escape');
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(page.getByRole('button',{name:'停止加速',exact:true})).toBeEnabled();
  await expect(connection).toHaveText('游戏连接：需检查');
  await page.mouse.move(0,0);await page.getByRole('button',{name:'停止加速',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('尚未取得恢复权限，将保留游戏接入');
  await page.getByRole('button',{name:'退出程序',exact:true}).hover();
  await expect(page.getByRole('tooltip')).toContainText('直接退出会保留接入');
  await page.route('**/api/hosts/prepare',route=>route.fulfill({status:400,json:{error:'管理员授权已取消',code:'uac_canceled'}}));
  await connection.hover();
  await popup.getByRole('button',{name:'修复',exact:true}).click();
  await expect(popup.getByRole('button',{name:'修复',exact:true})).toBeEnabled();
  await expect(connection).toHaveText('游戏连接：需检查');
  await expect(page.getByRole('listitem',{name:'管理员授权已取消',exact:true})).toHaveCount(0);
  await page.unroute('**/api/hosts/prepare');
  await page.keyboard.press('Escape');await expect(popup).toBeHidden();
  await connection.focus();
  await page.keyboard.press('Enter');await expect(popup).toBeVisible();
  await expect(popup.getByRole('button',{name:'修复',exact:true})).toBeFocused();
  await page.keyboard.press('Escape');await expect(popup).toBeHidden();
  await expect(connection).toBeFocused();
  await page.keyboard.press('Enter');await expect(popup.getByRole('button',{name:'修复',exact:true})).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(connection).toHaveText('游戏连接：已接入');
  await expect(page.getByRole('button',{name:'停止加速',exact:true})).toBeEnabled();
  expect(api.calls.filter(c=>c.path==='hosts/prepare')).toHaveLength(1);
  await navigate(page,'网络');
  await expect(page.getByRole('button',{name:'重新检测视频请求',exact:true})).toBeEnabled();
  expect(api.calls.filter(c=>c.path==='activation/enable')).toHaveLength(1);
});

for(const error of ['hosts 助手连接已中断，请重新打开应用后修复游戏连接','hosts 授权未完成（可能取消了 UAC）：无法连接助手'])test(`restore repair retains failure instructions: ${error}`,async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts={ready:true,managed:true,exitRestorePrepared:false,message:'已配置'};
  await page.route('**/api/hosts/prepare',route=>route.fulfill({status:400,json:{error}}));
  await page.goto('/');
  await page.getByRole('status',{name:'游戏连接',exact:true}).hover();
  const repair=page.getByRole('group',{name:'游戏连接说明',exact:true}).getByRole('button',{name:'修复',exact:true});
  await repair.click();
  await expect(page.getByRole('listitem',{name:error,exact:true})).toBeVisible();
  await expect(page.getByRole('status',{name:'游戏连接',exact:true})).toHaveText('游戏连接：需检查');
  await expect(repair).toBeEnabled();
  await expect(page.getByRole('button',{name:'停止加速',exact:true})).toBeEnabled();
});

for(const restore of [true,false])for(const queue of [true,false])test(`stop and resume follow active policies (${restore}, ${queue})`,async({page},info)=>{
  const api=await mockAPI(page);
  Object.assign(api.state.activeSettings,{restoreHostsOnExit:restore,queueWithGateway:queue});
  Object.assign(api.state.settings,{restoreHostsOnExit:!restore,queueWithGateway:!queue});
  await page.setViewportSize({width:restore?1280:320,height:900});await page.goto('/');
  const browserHint=page.getByText('可关闭浏览器，减少资源占用',{exact:true});
  await expect(browserHint).toHaveCount(0);
  const card=page.locator('#activation'),before=await card.boundingBox();
  const buttonBefore=await page.getByRole('button',{name:'开始加速',exact:true}).boundingBox();
  await expect(card.getByRole('link',{name:/检查连接|查看接入诊断/})).toHaveCount(0);
  await expect(card.getByRole('button',{name:'游戏接入说明',exact:true})).toHaveCount(0);
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  const stop=page.getByRole('button',{name:'停止加速',exact:true});
  await expect(stop).toBeEnabled();
  expect(await card.boundingBox()).toEqual(before);
  expect(await stop.boundingBox()).toEqual(buttonBefore);
  if(restore){
    await expect(browserHint).toBeVisible();
    const titleBox=(await card.getByRole('heading',{name:'加速运行中',exact:true}).boundingBox())!,hintBox=(await browserHint.boundingBox())!;
    expect(hintBox.x).toBeGreaterThan(titleBox.x+titleBox.width);
    expect(hintBox.y).toBeGreaterThanOrEqual(titleBox.y);
    expect(hintBox.y+hintBox.height).toBeLessThanOrEqual(titleBox.y+titleBox.height);
  }else await expect(browserHint).toBeHidden();
  await card.screenshot({path:info.outputPath('running-card.png'),animations:'disabled'});
  await page.mouse.move(0,0);await stop.hover();
  await expect(page.getByRole('tooltip')).toContainText(restore?'恢复本程序的游戏接入':'保留游戏接入');
  await stop.click();
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
  await expect(browserHint).toHaveCount(0);
  await expect(page.getByText('视频服务：未运行',{exact:true})).toBeVisible();
  await expect(page.getByText('房间队列：已关闭',{exact:true})).toBeVisible();
  await expect(page.getByText(`游戏连接：${restore?'未接入':'已接入'}`,{exact:true})).toBeVisible();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('stopped.png'),animations:'disabled'});
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(stop).toBeEnabled();
  await expect(page.getByText(`房间队列：${queue?'监控中':'已关闭'}`,{exact:true})).toBeVisible();
  expect(api.calls.filter(c=>c.path.startsWith('activation/')).map(c=>c.path)).toEqual(['activation/enable','activation/disable','activation/enable']);
  expect(api.calls.some(c=>c.path==='exit')).toBe(false);
});

test('stop failure reports the stopped gateway and preserved connection',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;
  await page.route('**/api/activation/disable',async route=>{
    const error='加速已停止，但未能恢复游戏直连：标记条目已被修改';
    api.state.running=false;api.state.activation={phase:'stopped',error,connectionError:error};
    await route.fulfill({status:400,json:{error}});
  });
  await page.goto('/');await page.getByRole('button',{name:'停止加速',exact:true}).click();
  await expect(page.getByRole('listitem',{name:/加速已停止，但未能恢复游戏直连/})).toBeVisible();
  await expect(page.getByText('视频服务：未运行',{exact:true})).toBeVisible();
  await expect(page.getByText('游戏连接：需检查',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'开始加速',exact:true})).toBeEnabled();
  await expect(page.getByRole('listitem',{name:'视频加速已停止。',exact:true})).toHaveCount(0);
});
