import {test,expect,type Page,type Locator} from '@playwright/test';
import {mockAPI} from './api';
import type {Status} from '../src/types';

const labels=['后台程序','游戏连接','视频服务','房间队列'];
const badge=(page:Page,label:string)=>page.getByRole('status',{name:label,exact:true});
const background=(element:Locator)=>element.evaluate(el=>getComputedStyle(el).backgroundColor);
const healthy=async(page:Page)=>{
  for(const [label,state] of [['后台程序','已连接'],['游戏连接','已接入'],['视频服务','运行中'],['房间队列','监控中']])await expect(badge(page,label)).toHaveText(label+'：'+state);
};

for(const [width,theme,forced] of [[1280,'light',false],[390,'dark',false],[320,'light',true]] as const)test(`runtime states share Fluent status styling at ${width}px ${theme}`,async({page},info)=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;api.state.queue={running:true,desired:true};
  await page.setViewportSize({width,height:950});await page.emulateMedia({colorScheme:theme,forcedColors:forced?'active':'none'});await page.goto('/#/home');await healthy(page);
  await expect(page.locator('.condition-strip [role="status"]')).toHaveText(['游戏连接：已接入','视频服务：运行中','房间队列：监控中']);
  const styles=[];
  for(const label of labels){
    const item=badge(page,label);await expect(item).toHaveAttribute('aria-atomic','true');await expect(item.locator('svg')).toHaveAttribute('aria-hidden','true');
    styles.push(await item.evaluate(el=>{const s=getComputedStyle(el);return {background:s.backgroundColor,color:s.color,font:s.fontSize,height:el.getBoundingClientRect().height};}));
  }
  expect(new Set(styles.map(style=>JSON.stringify(style))).size).toBe(1);expect(styles[0].background).not.toBe(styles[0].color);
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:info.outputPath('runtime-states.png'),fullPage:true,animations:'disabled'});
  const service=badge(page,'视频服务'),tip=page.getByRole('tooltip');
  await service.hover();await expect(tip).toContainText('HTTP 80 端口：正在监听；HTTPS 443 端口：正在转发');
  await expect(service).toHaveAccessibleDescription(/HTTP 80.*HTTPS 443/);
  await page.screenshot({path:info.outputPath('service-ports.png'),animations:'disabled'});
  await page.keyboard.press('Escape');await expect(tip).toBeHidden();
  await page.mouse.move(0,0);await service.focus();await expect(tip).toContainText('HTTPS 443');
  await page.keyboard.press('Escape');await expect(tip).toBeHidden();
});

test('failed polling invalidates running badges and recovery restores them',async({page})=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;api.state.queue={running:true,desired:true};
  await page.clock.install();await page.goto('/#/home');await healthy(page);
  const green=await background(badge(page,'后台程序'));
  await page.route('**/api/status',route=>route.fulfill({status:503,json:{error:'offline'}}));await page.clock.fastForward(5100);
  await expect(badge(page,'后台程序')).toHaveText('后台程序：更新失败');
  for(const label of labels.slice(1))await expect(badge(page,label)).toHaveText(label+'：待确认');
  await badge(page,'视频服务').focus();
  await expect(page.getByRole('tooltip')).toContainText('当前无法确认程序状态');
  await expect(page.getByRole('tooltip')).not.toContainText('正在监听');
  await page.keyboard.press('Escape');
  expect(await background(badge(page,'游戏连接'))).not.toBe(green);expect(await background(badge(page,'后台程序'))).not.toBe(green);
  await page.unroute('**/api/status');await page.clock.fastForward(5100);await healthy(page);
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  await expect(badge(page,'后台程序')).toHaveText('后台程序：未连接');
  for(const label of labels.slice(1))await expect(badge(page,label)).toHaveText(label+'：待确认');
});

test('conflicts, inactive services and startup have distinct semantic colors',async({page},info)=>{
  const api=await mockAPI(page);api.state.hosts.conflict=true;api.state.queue={desired:true,running:false};
  await page.clock.install();await page.goto('/#/home');
  await expect(badge(page,'后台程序')).toHaveText('后台程序：已连接');await expect(badge(page,'游戏连接')).toHaveText('游戏连接：接入冲突');
  await expect(badge(page,'视频服务')).toHaveText('视频服务：未运行');
  await badge(page,'视频服务').focus();
  await expect(page.getByRole('tooltip')).toContainText('HTTP 80 端口可用；HTTPS 443 端口可用');
  await page.keyboard.press('Escape');
  await expect(badge(page,'房间队列')).toHaveText('房间队列：未就绪');
  const colors=await Promise.all(labels.map(label=>background(badge(page,label))));expect(new Set(colors).size).toBe(4);
  await page.screenshot({path:info.outputPath('mixed-states.png'),fullPage:true,animations:'disabled'});
  api.state.activation={phase:'starting'};await page.clock.fastForward(5100);await expect(badge(page,'视频服务')).toHaveText('视频服务：启动中');
  expect(colors).not.toContain(await background(badge(page,'视频服务')));
  api.state.activation={};api.state.running=false;api.state.cdnError='视频服务启动失败';
  await page.clock.fastForward(5100);await expect(badge(page,'视频服务')).toHaveText('视频服务：异常');
  expect(await background(badge(page,'视频服务'))).toBe(colors[1]);
  api.state.actionErrors={queue:'无法启动队列'};await page.clock.fastForward(5100);
  await expect(badge(page,'房间队列')).toHaveText('房间队列：启动失败');expect(await background(badge(page,'房间队列'))).toBe(colors[1]);
  api.state.actionErrors={};api.state.queue={desired:true,running:true,logError:'无法读取游戏日志'};await page.clock.fastForward(5100);
  await expect(badge(page,'房间队列')).toHaveText('房间队列：需检查');expect(await background(badge(page,'房间队列'))).toBe(colors[3]);
  api.state.queue={desired:false,running:false};await page.clock.fastForward(5100);
  await expect(badge(page,'房间队列')).toHaveText('房间队列：已关闭');expect(await background(badge(page,'房间队列'))).toBe(colors[2]);
});

test('a rejected action does not misreport the backend connection',async({page})=>{
  await mockAPI(page);await page.route('**/api/exit',route=>route.fulfill({status:400,json:{error:'退出操作失败'}}));await page.goto('/#/home');
  await expect(badge(page,'后台程序')).toHaveText('后台程序：已连接');const green=await background(badge(page,'后台程序'));
  await page.getByRole('button',{name:'退出程序',exact:true}).click();
  await expect(page.getByRole('listitem',{name:'退出操作失败',exact:true})).toBeVisible();await expect(badge(page,'后台程序')).toHaveText('后台程序：已连接');
  expect(await background(badge(page,'后台程序'))).toBe(green);
});

const failures:{name:string;state:Partial<Status>;label:string;text:string;reason:string}[]=[
  {name:'UAC refusal',state:{activation:{phase:'failed',error:'游戏接入失败：管理员授权已取消',connectionError:'游戏接入失败：管理员授权已取消'}},label:'游戏连接',text:'接入失败',reason:'管理员授权已取消'},
  {name:'occupied port',state:{portOK:false,portOwner:{name:'fixture-player',pid:1234}},label:'视频服务',text:'异常',reason:'HTTP 80 端口被 fixture-player（PID 1234）占用'},
  {name:'reserved HTTPS port',state:{httpsPortOK:false},label:'视频服务',text:'异常',reason:'HTTPS 443 端口不可用或被系统保留'},
  {name:'queue startup failure',state:{running:true,hosts:{ready:true,message:'已接入'},queue:{desired:true,running:false},actionErrors:{queue:'无法读取 VRChat 日志目录：目录不存在'}},label:'房间队列',text:'启动失败',reason:'无法读取 VRChat 日志目录：目录不存在'},
  {name:'log read failure',state:{running:true,hosts:{ready:true,message:'已接入'},queue:{desired:true,running:true,file:'output_log.txt',logError:'日志读取失败：访问被拒绝'}},label:'房间队列',text:'需检查',reason:'日志读取失败：访问被拒绝'},
];

for(const failure of failures)test(`component reason supports hover and keyboard: ${failure.name}`,async({page},info)=>{
  const api=await mockAPI(page);Object.assign(api.state,failure.state);
  await page.setViewportSize({width:390,height:850});await page.goto('/');
  const target=badge(page,failure.label),tip=page.getByRole('tooltip');
  await expect(target).toHaveText(failure.label+'：'+failure.text);
  await expect(page.getByText('需要检查连接',{exact:true})).toHaveCount(0);
  await expect(page.getByRole('button',{name:'加速错误',exact:true})).toHaveCount(0);
  await target.hover();await expect(tip).toContainText(failure.reason);
  await expect(target).toHaveAccessibleDescription(new RegExp(failure.reason.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')));
  const box=(await tip.boundingBox())!;expect(box.x).toBeGreaterThanOrEqual(0);expect(box.x+box.width).toBeLessThanOrEqual(390);
  await page.screenshot({path:info.outputPath('component-reason.png'),animations:'disabled'});
  await page.keyboard.press('Escape');await expect(tip).toBeHidden();
  await page.mouse.move(0,0);await target.focus();await expect(tip).toContainText(failure.reason);
  await page.keyboard.press('Escape');await expect(tip).toBeHidden();
  if(failure.label==='房间队列'){
    await expect(badge(page,'游戏连接')).toHaveText('游戏连接：已接入');
    await expect(badge(page,'视频服务')).toHaveText('视频服务：运行中');
  }
});

test('queue monitoring stays healthy while waiting and explains its current log on hover',async({page},info)=>{
  const api=await mockAPI(page);api.state.running=true;api.state.hosts.ready=true;api.state.queue={desired:true,running:true,file:''};
  await page.setViewportSize({width:390,height:850});await page.clock.install();await page.goto('/');
  const target=badge(page,'房间队列'),tip=page.getByRole('tooltip');
  await healthy(page);const green=await background(badge(page,'视频服务'));
  expect(await background(target)).toBe(green);
  await target.hover();await expect(tip).toContainText('日志目录可读取，尚未发现 VRChat 日志，等待游戏生成日志。');
  await expect(target).toHaveAccessibleDescription(/等待游戏生成日志/);
  await page.screenshot({path:info.outputPath('waiting-for-log.png'),animations:'disabled'});
  await page.keyboard.press('Escape');await expect(tip).toBeHidden();
  await page.mouse.move(0,0);await target.focus();await expect(tip).toContainText('等待游戏生成日志');
  api.state.queue.file='output_log_2020-01-01.txt';await page.clock.fastForward(5100);
  await expect(tip).toContainText('正在监控日志：output_log_2020-01-01.txt。等待新的房间队列同步。');
  await healthy(page);expect(await background(target)).toBe(green);
  api.state.queue.songs=[{songId:1},{songId:2}];await page.clock.fastForward(5100);
  await expect(tip).toContainText('当前房间队列 2 首');
  api.state.queue.logError='日志读取失败：访问被拒绝';await page.clock.fastForward(5100);
  await expect(target).toHaveText('房间队列：需检查');expect(await background(target)).not.toBe(green);
  await expect(tip).toContainText('日志读取失败：访问被拒绝');
  api.state.queue.logError='';await page.clock.fastForward(5100);
  await healthy(page);expect(await background(target)).toBe(green);await expect(tip).toContainText('当前房间队列 2 首');
});

test('connection retry clears its reason and unknown status does not expose stale causes',async({page})=>{
  const api=await mockAPI(page);api.state.activation={phase:'failed',error:'游戏接入失败：授权已取消',connectionError:'游戏接入失败：授权已取消'};
  await page.clock.install();await page.goto('/');
  const connection=badge(page,'游戏连接');await expect(connection).toHaveText('游戏连接：接入失败');
  const failedColor=await background(connection);
  await page.route('**/api/status',route=>route.fulfill({status:503,json:{error:'offline'}}));await page.clock.fastForward(5100);
  await expect(connection).toHaveText('游戏连接：待确认');await connection.focus();
  await expect(page.getByRole('tooltip')).toContainText('当前无法确认程序状态');
  await expect(page.getByRole('tooltip')).not.toContainText('授权已取消');
  await page.unroute('**/api/status');await page.clock.fastForward(5100);
  await page.getByRole('button',{name:'开始加速',exact:true}).click();
  await expect(connection).toHaveText('游戏连接：已接入');expect(await background(connection)).not.toBe(failedColor);
  await connection.focus();await expect(page.getByRole('tooltip')).toContainText('可能请求 Windows 管理员授权');
  await expect(page.getByRole('tooltip')).not.toContainText('授权已取消');
});
